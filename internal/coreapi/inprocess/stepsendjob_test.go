package inprocess

import (
	"testing"

	"github.com/google/uuid"
)

func TestReplySubjectStep1Verbatim(t *testing.T) {
	if got := replySubject(1, "Intro", ""); got != "Intro" {
		t.Fatalf("step 1 subject should be verbatim, got %q", got)
	}
}

func TestReplySubjectEmptyStep2UsesThreadSubject(t *testing.T) {
	// A5: step >1 with an empty subject replies in-thread on the step-1 subject.
	if got := replySubject(2, "", "Intro"); got != "Re: Intro" {
		t.Fatalf("empty step-2 subject should reply on thread subject, got %q", got)
	}
}

func TestReplySubjectNonEmptyStep2Verbatim(t *testing.T) {
	// A5: a non-empty later-step subject is a deliberate new subject, used as-is
	// (threading is carried by In-Reply-To/References, not the subject).
	if got := replySubject(2, "Follow up", "Intro"); got != "Follow up" {
		t.Fatalf("non-empty step-2 subject should be verbatim, got %q", got)
	}
	if got := replySubject(3, "Re: Intro", "Intro"); got != "Re: Intro" {
		t.Fatalf("non-empty subject must not be double-prefixed, got %q", got)
	}
}

func TestDecodeCustomStringsAndCoercion(t *testing.T) {
	m := decodeCustom([]byte(`{"city":"London","seats":3,"vip":true}`))
	if m["city"] != "London" {
		t.Fatalf("city = %q", m["city"])
	}
	if m["seats"] != "3" {
		t.Fatalf("seats coercion = %q", m["seats"])
	}
	if m["vip"] != "true" {
		t.Fatalf("vip coercion = %q", m["vip"])
	}
}

func TestDecodeCustomEmptyAndInvalid(t *testing.T) {
	if decodeCustom(nil) != nil {
		t.Fatal("nil bytes should decode to nil map")
	}
	if decodeCustom([]byte("not json")) != nil {
		t.Fatal("invalid json should decode to nil map")
	}
}

// TestDeriveStepSendIDDeterministic proves a retried/raced advance for the
// same (campaign, contact, step_order) recomputes the identical send id, so
// every copy of a step's tracking tokens (embedded before the sends row
// exists) resolves to the one canonical row rather than a dead id.
func TestDeriveStepSendIDDeterministic(t *testing.T) {
	campaignID, contactID := uuid.New(), uuid.New()
	first := deriveStepSendID(campaignID, contactID, 2)
	second := deriveStepSendID(campaignID, contactID, 2)
	if first != second {
		t.Fatalf("expected the same (campaign, contact, step) to derive the same id, got %s vs %s", first, second)
	}
}

// TestDeriveStepSendIDDiffersByStepOrder proves distinct steps for the same
// enrollment don't collide onto one send id.
func TestDeriveStepSendIDDiffersByStepOrder(t *testing.T) {
	campaignID, contactID := uuid.New(), uuid.New()
	step1 := deriveStepSendID(campaignID, contactID, 1)
	step2 := deriveStepSendID(campaignID, contactID, 2)
	if step1 == step2 {
		t.Fatalf("different step orders must derive different ids, both got %s", step1)
	}
}

// TestDeriveStepSendIDDiffersByContact proves two contacts on the same
// campaign/step don't collide onto one send id.
func TestDeriveStepSendIDDiffersByContact(t *testing.T) {
	campaignID := uuid.New()
	a := deriveStepSendID(campaignID, uuid.New(), 1)
	b := deriveStepSendID(campaignID, uuid.New(), 1)
	if a == b {
		t.Fatalf("different contacts must derive different ids, both got %s", a)
	}
}
