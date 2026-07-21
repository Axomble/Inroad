package inprocess

import "testing"

func TestReplySubjectStep1Verbatim(t *testing.T) {
	if got := replySubject(1, "Intro"); got != "Intro" {
		t.Fatalf("step 1 subject should be verbatim, got %q", got)
	}
}

func TestReplySubjectPrefixesFromStep2(t *testing.T) {
	if got := replySubject(2, "Following up"); got != "Re: Following up" {
		t.Fatalf("got %q", got)
	}
}

func TestReplySubjectNoDoubleRe(t *testing.T) {
	if got := replySubject(3, "Re: Intro"); got != "Re: Intro" {
		t.Fatalf("got %q", got)
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
