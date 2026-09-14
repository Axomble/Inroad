package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirmYesFlagSkipsPromptEntirely(t *testing.T) {
	out := &bytes.Buffer{}
	// autoYes=true must not even attempt to read `in`: passing an already-
	// exhausted reader (empty string) proves nothing was read from it.
	if !confirm(strings.NewReader(""), out, "Proceed?", true) {
		t.Fatal("expected confirm(autoYes=true) to return true")
	}
	if out.Len() != 0 {
		t.Fatalf("expected no prompt printed with --yes, got %q", out.String())
	}
}

func TestConfirmAcceptsYAndYes(t *testing.T) {
	for _, answer := range []string{"y", "Y", "yes", "YES", " y \n"} {
		out := &bytes.Buffer{}
		if !confirm(strings.NewReader(answer+"\n"), out, "Proceed?", false) {
			t.Errorf("answer %q: expected confirm to return true", answer)
		}
	}
}

func TestConfirmRejectsAnythingElse(t *testing.T) {
	for _, answer := range []string{"n", "no", "", "yesplease", "\n"} {
		out := &bytes.Buffer{}
		if confirm(strings.NewReader(answer+"\n"), out, "Proceed?", false) {
			t.Errorf("answer %q: expected confirm to return false", answer)
		}
	}
}

func TestConfirmPrintsThePromptWithYNSuffix(t *testing.T) {
	out := &bytes.Buffer{}
	confirm(strings.NewReader("n\n"), out, "Delete everything?", false)
	if !strings.Contains(out.String(), "Delete everything? [y/N]: ") {
		t.Fatalf("expected the prompt with [y/N] suffix, got %q", out.String())
	}
}
