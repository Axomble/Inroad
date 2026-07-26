package warmup

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestStaticLibraryStableSelection(t *testing.T) {
	lib := NewStaticLibrary()
	ctx := context.Background()

	first, err := lib.Thread(ctx, "seed-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := lib.Thread(ctx, "seed-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same seed produced different threads:\n%+v\n%+v", first, second)
	}
}

func TestStaticLibraryGreetingSubstituted(t *testing.T) {
	lib := NewStaticLibrary()
	// Sweep many seeds so we exercise every thread and greeting combination.
	for i := 0; i < 200; i++ {
		th, err := lib.Thread(context.Background(), string(rune('a'+i%26))+"-seed-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(th.Turns) == 0 {
			t.Fatal("thread has no turns")
		}
		if strings.Contains(th.Subject, greetingToken) {
			t.Fatalf("greeting token not substituted in subject: %q", th.Subject)
		}
		opener := th.Turns[0]
		if strings.Contains(opener, greetingToken) {
			t.Fatalf("greeting token not substituted in opener: %q", opener)
		}
		if !startsWithGreeting(opener) {
			t.Fatalf("opener does not start with a known greeting: %q", opener)
		}
	}
}

func TestReplyExhaustion(t *testing.T) {
	th, err := NewStaticLibrary().Thread(context.Background(), "seed-reply")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := range th.Turns {
		body, ok := Reply(th, i)
		if !ok {
			t.Fatalf("expected turn %d to exist", i)
		}
		if body != th.Turns[i] {
			t.Fatalf("turn %d body mismatch", i)
		}
	}
	if _, ok := Reply(th, len(th.Turns)); ok {
		t.Fatal("expected exhaustion past the last turn")
	}
	if _, ok := Reply(th, -1); ok {
		t.Fatal("expected negative turn to be rejected")
	}
}

func TestEmptyLibraryReturnsError(t *testing.T) {
	var empty StaticLibrary // zero value: no threads
	if _, err := empty.Thread(context.Background(), "seed"); !errors.Is(err, ErrNoContent) {
		t.Fatalf("expected ErrNoContent, got %v", err)
	}
}

func startsWithGreeting(s string) bool {
	for _, g := range greetings {
		if strings.HasPrefix(s, g) {
			return true
		}
	}
	return false
}
