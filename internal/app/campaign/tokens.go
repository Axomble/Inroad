package campaign

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// builtinTokens are the placeholders personalize.substitute replaces from the
// contact's own columns. The list is duplicated here rather than exported from
// the personalizer because the two answer different questions -- that package
// SUBSTITUTES, this one VALIDATES -- and the worker's copy must stay free of
// any control-plane dependency. tokens_test.go asserts the two agree, so the
// duplication cannot rot silently.
var builtinTokens = []string{"first_name", "last_name", "email", "company"}

// tokenRE matches any {{...}} placeholder, deliberately more permissive than
// the personalizer's own patterns: it has to see the tokens that will NOT be
// substituted, which is the entire point of this check. `{{ first_name }}` with
// spaces is one of them -- personalize does a literal string replace, so the
// spaced form survives into the sent email untouched.
var tokenRE = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// customPrefix addresses a workspace-defined field: {{custom.<key>}}.
const customPrefix = "custom."

// UnknownTokens returns the distinct placeholders in the given templates that
// nothing will substitute, sorted for a stable message.
//
// Both ways of being unknown are silent at send time, which is why they are
// caught here rather than left to the send path's log line:
//
//   - A token matching NO substitution rule ({{firstname}}, {{ first_name }})
//     is left in the body exactly as typed and mailed to the prospect verbatim.
//   - A token shaped like {{custom.<key>}} whose key has no definition IS
//     matched by the personalizer and replaced with the empty string, so the
//     email goes out reading "Hi , at ".
//
// Neither is visible in the editor, and both are permanent once delivered.
//
// liveKeys is the set of custom field keys the workspace currently defines.
// Keys are matched case-sensitively, matching personalize's map lookup: the
// definitions table constrains keys to lower-case precisely so that {{custom.Industry}}
// is unambiguously a typo rather than a second spelling of a real field.
func UnknownTokens(liveKeys []string, templates ...string) []string {
	known := make(map[string]struct{}, len(liveKeys))
	for _, k := range liveKeys {
		known[k] = struct{}{}
	}
	seen := map[string]struct{}{}
	var unknown []string
	for _, tmpl := range templates {
		for _, match := range tokenRE.FindAllString(tmpl, -1) {
			if _, dup := seen[match]; dup {
				continue
			}
			seen[match] = struct{}{}
			if tokenResolves(match, known) {
				continue
			}
			unknown = append(unknown, match)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// tokenResolves reports whether personalize will substitute this exact literal.
func tokenResolves(match string, known map[string]struct{}) bool {
	name := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
	if slices.Contains(builtinTokens, name) {
		return true
	}
	key, ok := strings.CutPrefix(name, customPrefix)
	if !ok {
		return false
	}
	_, defined := known[key]
	return defined
}
