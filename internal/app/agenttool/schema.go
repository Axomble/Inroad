package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Page-size bounds shared by every list-shaped tool. One pair of numbers for
// the whole surface so a model that learns the limit on one tool has learned
// it everywhere; every list tool repeats them in its description.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// jsonObject marshals its pairs in insertion order. encoding/json sorts map
// keys, which would push loading_message out of first position and reshuffle a
// schema's properties between builds — the tool list is sent verbatim to the
// provider and prompt caching depends on it being byte-stable.
type jsonObject []jsonPair

type jsonPair struct {
	key string
	val any
}

func (o jsonObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(p.key)
		if err != nil {
			return nil, fmt.Errorf("marshal key %q: %w", p.key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(p.val)
		if err != nil {
			return nil, fmt.Errorf("marshal value for %q: %w", p.key, err)
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// field is one property of a tool's input schema.
type field struct {
	name     string
	schema   jsonObject
	required bool
}

func strField(name, desc string, required bool) field {
	return field{name: name, schema: jsonObject{{"type", "string"}, {"description", desc}}, required: required}
}

func methodField(desc string, values []string) field {
	return field{name: "method", schema: jsonObject{
		{"type", "string"}, {"description", desc}, {"enum", values},
	}, required: true}
}

// limitField is the page-size property every list-shaped tool exposes.
func limitField() field {
	return field{name: "limit", schema: jsonObject{
		{"type", "integer"},
		{"description", fmt.Sprintf("How many results to return. Defaults to %d, maximum %d.", defaultLimit, maxLimit)},
		{"minimum", 1},
		{"maximum", maxLimit},
	}}
}

func offsetField() field {
	return field{name: "offset", schema: jsonObject{
		{"type", "integer"},
		{"description", "How many results to skip, for paging past the first page. Defaults to 0."},
		{"minimum", 0},
	}}
}

// mustSchema builds a JSON Schema object from fields. It panics on a schema
// this package itself could not marshal: tool schemas are package-literal
// constants built at construction time, so a failure here is a programming
// error that must not be discoverable only at request time.
func mustSchema(fields ...field) json.RawMessage {
	props := make(jsonObject, 0, len(fields))
	required := make([]string, 0, len(fields))
	for _, f := range fields {
		props = append(props, jsonPair{f.name, f.schema})
		if f.required {
			required = append(required, f.name)
		}
	}
	out := jsonObject{
		{"type", "object"},
		{"properties", props},
		{"required", required},
		{"additionalProperties", false},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		panic(fmt.Sprintf("agenttool: build input schema: %v", err))
	}
	return raw
}

// loadingMessageSchema is the property the registry injects into every tool.
var loadingMessageSchema = jsonObject{
	{"type", "string"},
	{"description", "A short present-tense sentence naming what THIS call is doing, shown to the user while it runs (\"Reading the Q3 outbound campaign\", \"Pausing campaign Q3 outbound\"). Name the specific record, not the tool."},
}

// withLoadingMessage returns schema with LoadingMessageProperty spliced in as
// its first property and marked required. It re-emits the top level in a fixed
// key order so the result is byte-stable across builds.
//
// The splice is textual on the serialized "properties" object because Go maps
// do not preserve order: decoding into a map and re-encoding would sort the
// properties and lose the leading position the convention depends on.
func withLoadingMessage(schema json.RawMessage) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(schema, &top); err != nil {
		return nil, fmt.Errorf("input schema is not a JSON object: %w", err)
	}
	var kind string
	if raw, ok := top["type"]; ok {
		if err := json.Unmarshal(raw, &kind); err != nil {
			return nil, fmt.Errorf("input schema type is not a string: %w", err)
		}
	}
	if kind != "object" {
		return nil, fmt.Errorf("input schema type must be %q, got %q", "object", kind)
	}

	inner := ""
	if raw, ok := top["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(raw, &props); err != nil {
			return nil, fmt.Errorf("input schema properties is not an object: %w", err)
		}
		if _, declared := props[LoadingMessageProperty]; declared {
			return nil, fmt.Errorf("tool declares %s itself; the registry injects it", LoadingMessageProperty)
		}
		trimmed := strings.TrimSpace(string(raw))
		inner = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}

	lm, err := json.Marshal(loadingMessageSchema)
	if err != nil {
		return nil, fmt.Errorf("marshal %s schema: %w", LoadingMessageProperty, err)
	}
	var props bytes.Buffer
	props.WriteString(`{"` + LoadingMessageProperty + `":`)
	props.Write(lm)
	if inner != "" {
		props.WriteByte(',')
		props.WriteString(inner)
	}
	props.WriteByte('}')

	required := []string{LoadingMessageProperty}
	if raw, ok := top["required"]; ok {
		var existing []string
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("input schema required is not a string array: %w", err)
		}
		required = append(required, existing...)
	}

	out := jsonObject{
		{"type", "object"},
		{"properties", json.RawMessage(props.Bytes())},
		{"required", required},
	}
	if raw, ok := top["additionalProperties"]; ok {
		out = append(out, jsonPair{"additionalProperties", raw})
	}
	// Anything else the tool declared (descriptions, $defs) is preserved after
	// the known keys, in a deterministic order.
	rest := make([]string, 0, len(top))
	for k := range top {
		switch k {
		case "type", "properties", "required", "additionalProperties":
		default:
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, jsonPair{k, top[k]})
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal injected schema: %w", err)
	}
	return raw, nil
}

// baseArgs carries the injected property so every tool's argument struct can
// decode with unknown fields rejected — a model that invents a property gets
// told so instead of having it silently dropped.
type baseArgs struct {
	LoadingMessage string `json:"loading_message"`
}

// decodeArgs unmarshals args into dst, rejecting unknown properties. A decode
// failure is a Result, not an error: the model can fix its own arguments.
func decodeArgs(args json.RawMessage, dst any) *Result {
	raw := bytes.TrimSpace(args)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		r := Fail(fmt.Sprintf(
			"could not read the arguments (%s); re-read this tool's input schema and call it again using only the properties it documents",
			err))
		return &r
	}
	return nil
}

// resolveLimit applies the shared page-size bounds. An explicitly out-of-range
// limit is rejected rather than clamped, so a model asking for 500 rows learns
// it cannot have them instead of silently receiving 100.
func resolveLimit(v *int) (int, *Result) {
	if v == nil {
		return defaultLimit, nil
	}
	if *v < 1 || *v > maxLimit {
		r := Fail(fmt.Sprintf("limit must be between 1 and %d; call again with a smaller limit and page with offset if you need more", maxLimit))
		return 0, &r
	}
	return *v, nil
}

func resolveOffset(v *int) (int, *Result) {
	if v == nil {
		return 0, nil
	}
	if *v < 0 {
		r := Fail("offset must be zero or greater")
		return 0, &r
	}
	return *v, nil
}

// parseID turns a model-supplied id string into a UUID, naming the property in
// the recovery instruction so the model knows which argument to fix.
func parseID(property, value string) (uuid.UUID, *Result) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		r := Fail(fmt.Sprintf(
			"%s is not a valid id; ids come from a previous read tool result — call inroad_search or the matching *_read tool with method=list to get one",
			property))
		return uuid.Nil, &r
	}
	return id, nil
}

// unknownMethod is the shared recovery instruction for a method argument
// outside a tool's enum.
func unknownMethod(got string, allowed []string) Result {
	return Fail(fmt.Sprintf("unknown method %q; this tool accepts method=%s", got, strings.Join(allowed, "|")))
}
