package inbox

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHighlightSegments(t *testing.T) {
	m := func(s string) HighlightSegment { return HighlightSegment{Text: s, Match: true} }
	p := func(s string) HighlightSegment { return HighlightSegment{Text: s} }
	// Cases are written with [ and ] standing for the start/stop markers, which
	// are invisible private-use code points.
	markers := strings.NewReplacer("[", highlightStart, "]", highlightStop)
	cases := []struct {
		name   string
		marked string
		want   []HighlightSegment
	}{
		{"empty", "", nil},
		{"no match", "plain text", []HighlightSegment{p("plain text")}},
		{"one match mid-text", "our [pricing] is fair", []HighlightSegment{p("our "), m("pricing"), p(" is fair")}},
		{"match at both ends", "[a] b [c]", []HighlightSegment{m("a"), p(" b "), m("c")}},
		{"adjacent matches", "[net] [thirty]", []HighlightSegment{m("net"), p(" "), m("thirty")}},
		{"empty match dropped", "x[]y", []HighlightSegment{p("x"), p("y")}},
		{"unterminated match keeps its text", "x [tail", []HighlightSegment{p("x "), m("tail")}},
		{"stray stop marker stripped", "x]y", []HighlightSegment{p("xy")}},
		{"nested start stripped", "[a[b]", []HighlightSegment{m("ab")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := highlightSegments(markers.Replace(tc.marked))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("highlightSegments(%q) = %#v, want %#v", tc.marked, got, tc.want)
			}
			for _, s := range got {
				if strings.ContainsAny(s.Text, highlightStart+highlightStop) {
					t.Fatalf("segment %q leaks a marker", s.Text)
				}
			}
		})
	}
}

func TestNormalizeSearchText(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"trimmed", "  pricing  ", "pricing", false},
		{"operators pass through untouched", `"net thirty" -x OR y`, `"net thirty" -x OR y`, false},
		{"stop words only is not an error", "the", "the", false},
		{"empty", "", "", true},
		{"whitespace only", " \t\n ", "", true},
		{"NUL", "a\x00b", "", true},
		{"invalid UTF-8", "a\xffb", "", true},
		{"at the cap, counted in characters", strings.Repeat("é", MaxSearchQueryLength), strings.Repeat("é", MaxSearchQueryLength), false},
		{"over the cap", strings.Repeat("a", MaxSearchQueryLength+1), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSearchText(tc.raw)
			if tc.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("err = %v, want ErrValidation", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("normalizeSearchText(%q) = %q, %v; want %q, nil", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeSearchLimit(t *testing.T) {
	for requested, want := range map[int32]int32{
		0: DefaultSearchPageLimit, -3: DefaultSearchPageLimit, 1: 1,
		MaxSearchPageLimit: MaxSearchPageLimit, MaxSearchPageLimit + 1: MaxSearchPageLimit, 10_000: MaxSearchPageLimit,
	} {
		if got := normalizeSearchLimit(requested); got != want {
			t.Errorf("normalizeSearchLimit(%d) = %d, want %d", requested, got, want)
		}
	}
}
