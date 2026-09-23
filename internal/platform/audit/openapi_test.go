package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The viewer's TypeScript types are generated from api/openapi.yaml, so an
// action recorded here but missing from the spec's enum is one the UI's types
// say cannot exist. A line scan rather than a YAML parser: the two schemas are
// simple lists and this keeps the module free of a YAML dependency.
func TestOpenAPIEnumsMatchTheVocabulary(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("open spec: %v", err)
	}
	defer func() { _ = f.Close() }()

	var (
		lines []string
		sc    = bufio.NewScanner(f)
	)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read spec: %v", err)
	}

	gotActions := blockList(t, lines, "    AuditAction:")
	var wantActions []string
	for _, a := range AllActions {
		wantActions = append(wantActions, string(a))
	}
	if !slices.Equal(gotActions, wantActions) {
		t.Errorf("AuditAction enum = %v\nwant (audit.AllActions) %v", gotActions, wantActions)
	}

	gotActors := inlineEnum(t, lines, "    AuditActorType:")
	var wantActors []string
	for _, a := range AllActorTypes {
		wantActors = append(wantActors, string(a))
	}
	if !slices.Equal(gotActors, wantActors) {
		t.Errorf("AuditActorType enum = %v, want %v", gotActors, wantActors)
	}
}

// blockList returns the "        - x" entries of the enum under header.
func blockList(t *testing.T, lines []string, header string) []string {
	t.Helper()
	i := slices.Index(lines, header)
	if i < 0 {
		t.Fatalf("schema %q not found in the spec", strings.TrimSpace(header))
	}
	var out []string
	for _, l := range lines[i+1:] {
		if v, ok := strings.CutPrefix(l, "        - "); ok {
			out = append(out, strings.TrimSpace(v))
			continue
		}
		if len(out) > 0 {
			break
		}
		if !strings.HasPrefix(l, "      ") {
			break
		}
	}
	return out
}

// inlineEnum returns the values of the "enum: [a, b]" line under header.
func inlineEnum(t *testing.T, lines []string, header string) []string {
	t.Helper()
	i := slices.Index(lines, header)
	if i < 0 {
		t.Fatalf("schema %q not found in the spec", strings.TrimSpace(header))
	}
	for _, l := range lines[i+1:] {
		if !strings.HasPrefix(l, "      ") {
			break
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "enum: ["); ok {
			var out []string
			for _, s := range strings.Split(strings.TrimSuffix(v, "]"), ",") {
				out = append(out, strings.TrimSpace(s))
			}
			return out
		}
	}
	t.Fatalf("no inline enum under %q", strings.TrimSpace(header))
	return nil
}
