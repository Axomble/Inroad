package replyclassify

import (
	"context"
	"testing"
)

// fakeModel is an in-test ModelClassifier spy: it records call count and returns
// a fixed verdict. Used to prove Layer 3 runs only on the ambiguous middle and
// only when injected.
type fakeModel struct {
	ret   string
	ok    bool
	calls int
}

func (f *fakeModel) Classify(_ context.Context, _, _ string) (string, bool) {
	f.calls++
	return f.ret, f.ok
}

func TestClassifyHeaders(t *testing.T) {
	cls := New(nil)
	cases := []struct {
		name     string
		in       Input
		want     string
		wantSrc  string
		wantConf float64
	}{
		{
			name:     "OOO subject without Auto-Submitted",
			in:       Input{Subject: "Out of Office: back Monday", BodyText: "I am away."},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.98,
		},
		{
			name:     "automatic reply subject",
			in:       Input{Subject: "Automatic reply: On vacation", BodyText: "Away."},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.98,
		},
		{
			name:     "auto colon subject prefix",
			in:       Input{Subject: "Auto: Away from desk", BodyText: "x"},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.98,
		},
		{
			name:     "away from the office subject",
			in:       Input{Subject: "I'm away from the office until Aug 3", BodyText: "x"},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.98,
		},
		{
			name:     "Takeaway from is NOT out of office (boundary)",
			in:       Input{Subject: "Takeaway from our call", BodyText: "great to connect"},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
		{
			name:     "Auto-Submitted auto-replied is OOO",
			in:       Input{Headers: map[string][]string{"Auto-Submitted": {"auto-replied"}}, Subject: "Re: hi", BodyText: "x"},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.95,
		},
		{
			name:     "Auto-Submitted auto-generated is auto_reply",
			in:       Input{Headers: map[string][]string{"Auto-Submitted": {"auto-generated"}}, Subject: "Receipt", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.95,
		},
		{
			name:     "Auto-Submitted no is ignored",
			in:       Input{Headers: map[string][]string{"Auto-Submitted": {"no"}}, Subject: "Re: hi", BodyText: "let me think about it later"},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
		{
			name:     "lower-cased header key still matches",
			in:       Input{Headers: map[string][]string{"auto-submitted": {"auto-generated"}}, Subject: "x", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.95,
		},
		{
			name:     "Precedence bulk is auto_reply",
			in:       Input{Headers: map[string][]string{"Precedence": {"bulk"}}, Subject: "News", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.75,
		},
		{
			name:     "Precedence junk is auto_reply",
			in:       Input{Headers: map[string][]string{"Precedence": {"junk"}}, Subject: "News", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.75,
		},
		{
			name:     "Precedence list is auto_reply",
			in:       Input{Headers: map[string][]string{"Precedence": {"list"}}, Subject: "News", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.75,
		},
		{
			name:     "Precedence auto_reply is auto_reply",
			in:       Input{Headers: map[string][]string{"Precedence": {"auto_reply"}}, Subject: "x", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.9,
		},
		{
			name:     "X-Autoreply header is OOO",
			in:       Input{Headers: map[string][]string{"X-Autoreply": {"yes"}}, Subject: "Re: hi", BodyText: "x"},
			want:     ClassOutOfOffice,
			wantSrc:  SourceHeader,
			wantConf: 0.93,
		},
		{
			name:     "X-Auto-Response-Suppress is auto_reply",
			in:       Input{Headers: map[string][]string{"X-Auto-Response-Suppress": {"All"}}, Subject: "x", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.9,
		},
		{
			name:     "multipart report delivery-status is auto_reply",
			in:       Input{Headers: map[string][]string{"Content-Type": {"multipart/report; report-type=delivery-status; boundary=x"}}, Subject: "Undeliverable", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.95,
		},
		{
			name:     "multipart report disposition-notification is auto_reply",
			in:       Input{Headers: map[string][]string{"Content-Type": {"multipart/report; report-type=disposition-notification"}}, Subject: "Read", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.95,
		},
		{
			name:     "null Return-Path is auto_reply",
			in:       Input{Headers: map[string][]string{"Return-Path": {"<>"}}, Subject: "x", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.85,
		},
		{
			name:     "mailer-daemon From is auto_reply",
			in:       Input{Headers: map[string][]string{"From": {"Mail Delivery Subsystem <MAILER-DAEMON@example.com>"}}, Subject: "Undelivered", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.8,
		},
		{
			name:     "no-reply From is auto_reply",
			in:       Input{Headers: map[string][]string{"From": {"no-reply@example.com"}}, Subject: "Notice", BodyText: "x"},
			want:     ClassAutoReply,
			wantSrc:  SourceHeader,
			wantConf: 0.8,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cls.Classify(context.Background(), tc.in)
			if got.Class != tc.want || got.Source != tc.wantSrc || got.Confidence != tc.wantConf {
				t.Fatalf("Classify(%q) = {%s,%s,%.2f}, want {%s,%s,%.2f}",
					tc.name, got.Class, got.Source, got.Confidence, tc.want, tc.wantSrc, tc.wantConf)
			}
		})
	}
}

func TestClassifyLexicon(t *testing.T) {
	cls := New(nil)
	cases := []struct {
		name     string
		in       Input
		want     string
		wantSrc  string
		wantConf float64
	}{
		{
			name:     "not interested is negative (negation-order regression)",
			in:       Input{Subject: "Re: your pitch", BodyText: "Thanks but not interested."},
			want:     ClassNegative,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "I'm interested is positive (negation-aware allows)",
			in:       Input{Subject: "Re: your pitch", BodyText: "Hi, I'm interested — tell me more."},
			want:     ClassPositive,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "hot lead: please don't hesitate to send pricing is positive",
			in:       Input{Subject: "Re: your pitch", BodyText: "Very interested — please don't hesitate to send pricing."},
			want:     ClassPositive,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "no problem interested is positive (no is not a negator)",
			in:       Input{Subject: "Re: hi", BodyText: "no problem, interested — let's set up a call"},
			want:     ClassPositive,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "unsubscribe wins",
			in:       Input{Subject: "Re: hi", BodyText: "please unsubscribe me from this list"},
			want:     ClassUnsubscribe,
			wantSrc:  SourceLexicon,
			wantConf: 0.9,
		},
		{
			name:     "remove me is unsubscribe",
			in:       Input{Subject: "stop", BodyText: "remove me"},
			want:     ClassUnsubscribe,
			wantSrc:  SourceLexicon,
			wantConf: 0.9,
		},
		{
			name:     "please stop is unsubscribe (boundary stop)",
			in:       Input{Subject: "Re: hi", BodyText: "please stop"},
			want:     ClassUnsubscribe,
			wantSrc:  SourceLexicon,
			wantConf: 0.9,
		},
		{
			name:     "compliance-first: unsubscribe beats negative sentiment",
			in:       Input{Subject: "Re: hi", BodyText: "unsubscribe, I'm not interested in this"},
			want:     ClassUnsubscribe,
			wantSrc:  SourceLexicon,
			wantConf: 0.9,
		},
		{
			name:     "sounds great lets chat is positive",
			in:       Input{Subject: "Re: hi", BodyText: "sounds great, let's chat next week"},
			want:     ClassPositive,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "nonstop does not match unsubscribe (boundary)",
			in:       Input{Subject: "Re: hi", BodyText: "we run a nonstop operation here"},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
		{
			name:     "stopped by does not match unsubscribe (boundary)",
			in:       Input{Subject: "Re: hi", BodyText: "he stopped by yesterday to say hello"},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
		{
			name:     "no need is negative (boundary phrase)",
			in:       Input{Subject: "Re: hi", BodyText: "no need for this, thanks"},
			want:     ClassNegative,
			wantSrc:  SourceLexicon,
			wantConf: 0.8,
		},
		{
			name:     "empty input is unknown",
			in:       Input{},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
		{
			name:     "ambiguous text is unknown with nil model",
			in:       Input{Subject: "Re: hi", BodyText: "let me think about it and get back to you"},
			want:     ClassUnknown,
			wantSrc:  SourceNone,
			wantConf: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cls.Classify(context.Background(), tc.in)
			if got.Class != tc.want || got.Source != tc.wantSrc || got.Confidence != tc.wantConf {
				t.Fatalf("Classify(%q) = {%s,%s,%.2f}, want {%s,%s,%.2f}",
					tc.name, got.Class, got.Source, got.Confidence, tc.want, tc.wantSrc, tc.wantConf)
			}
		})
	}
}

// TestNegationLogic pins the exact behavior of the negation-aware positive scan,
// including the multi-occurrence continuation and the "n't"-suffix fallback.
func TestNegationLogic(t *testing.T) {
	cls := New(nil)
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{
			// "not interested" is a negative phrase and wins at Layer 2 step 2
			// before positive is even considered.
			name: "not interested but happy to chat resolves negative",
			in:   Input{BodyText: "not interested in X, but happy to chat about Y"},
			want: ClassNegative,
		},
		{
			// First "happy to chat" is negated by "not"; the second occurrence,
			// outside the negation window, still fires positive (continuation scan).
			name: "later non-negated occurrence still fires positive",
			in:   Input{BodyText: "not happy to chat earlier but happy to chat now"},
			want: ClassPositive,
		},
		{
			// "couldn't" is not in the explicit negators map; it is caught by the
			// "n't" suffix fallback, so the positive is suppressed -> unknown.
			name: "nt suffix word suppresses positive (suffix fallback)",
			in:   Input{BodyText: "couldn't we schedule a call sometime"},
			want: ClassUnknown,
		},
		{
			// With a 3-word window, "not now but interested later" keeps "not"
			// inside the window, so the positive is suppressed -> unknown.
			name: "not now but interested later is suppressed to unknown",
			in:   Input{BodyText: "not now but interested later"},
			want: ClassUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cls.Classify(context.Background(), tc.in); got.Class != tc.want {
				t.Fatalf("Classify(%q) = %s, want %s", tc.name, got.Class, tc.want)
			}
		})
	}
}

// TestLayerPriority pins that Layer 1 (headers) wins over Layer 2 (lexicon):
// an OOO-subject reply whose body also carries strong lexicon signals is still
// out_of_office.
func TestLayerPriority(t *testing.T) {
	got := New(nil).Classify(context.Background(), Input{
		Subject:  "Out of Office: back Monday",
		BodyText: "not interested, please stop",
	})
	if got.Class != ClassOutOfOffice || got.Source != SourceHeader {
		t.Fatalf("got {%s,%s}, want {out_of_office,header} (Layer 1 must win)", got.Class, got.Source)
	}
}

func TestLayer3ModelSeam(t *testing.T) {
	middle := Input{Subject: "Re: hi", BodyText: "let me think about it and get back to you"}

	t.Run("nil model resolves middle to unknown with no I/O", func(t *testing.T) {
		got := New(nil).Classify(context.Background(), middle)
		if got.Class != ClassUnknown || got.Source != SourceNone || got.Confidence != 0 {
			t.Fatalf("got {%s,%s,%.2f}, want {unknown,\"\",0}", got.Class, got.Source, got.Confidence)
		}
	})

	t.Run("injected model resolves the middle", func(t *testing.T) {
		fm := &fakeModel{ret: ClassPositive, ok: true}
		got := New(fm).Classify(context.Background(), middle)
		if got.Class != ClassPositive || got.Source != SourceModel || got.Confidence != 0.7 {
			t.Fatalf("got {%s,%s,%.2f}, want {positive,model,0.70}", got.Class, got.Source, got.Confidence)
		}
		if fm.calls != 1 {
			t.Fatalf("model calls = %d, want 1", fm.calls)
		}
	})

	t.Run("model returning ok=false falls back to unknown", func(t *testing.T) {
		fm := &fakeModel{ret: "", ok: false}
		got := New(fm).Classify(context.Background(), middle)
		if got.Class != ClassUnknown || got.Source != SourceNone {
			t.Fatalf("got {%s,%s}, want {unknown,\"\"}", got.Class, got.Source)
		}
	})

	t.Run("model returning out-of-space class is rejected", func(t *testing.T) {
		fm := &fakeModel{ret: ClassUnsubscribe, ok: true}
		got := New(fm).Classify(context.Background(), middle)
		if got.Class != ClassUnknown {
			t.Fatalf("got %s, want unknown (model may only return positive/negative/neutral)", got.Class)
		}
	})

	t.Run("model NOT called when headers decide", func(t *testing.T) {
		fm := &fakeModel{ret: ClassPositive, ok: true}
		in := Input{Headers: map[string][]string{"Auto-Submitted": {"auto-generated"}}, Subject: "x", BodyText: "x"}
		got := New(fm).Classify(context.Background(), in)
		if got.Class != ClassAutoReply {
			t.Fatalf("got %s, want auto_reply", got.Class)
		}
		if fm.calls != 0 {
			t.Fatalf("model calls = %d, want 0 (Layer 1 must short-circuit)", fm.calls)
		}
	})

	t.Run("model NOT called when lexicon decides", func(t *testing.T) {
		fm := &fakeModel{ret: ClassPositive, ok: true}
		in := Input{Subject: "Re: hi", BodyText: "not interested"}
		got := New(fm).Classify(context.Background(), in)
		if got.Class != ClassNegative {
			t.Fatalf("got %s, want negative", got.Class)
		}
		if fm.calls != 0 {
			t.Fatalf("model calls = %d, want 0 (Layer 2 must short-circuit)", fm.calls)
		}
	})
}

func TestIsAutomated(t *testing.T) {
	cases := map[string]bool{
		ClassAutoReply:   true,
		ClassOutOfOffice: true,
		ClassPositive:    false,
		ClassNegative:    false,
		ClassNeutral:     false,
		ClassUnsubscribe: false,
		ClassUnknown:     false,
	}
	for class, want := range cases {
		if got := IsAutomated(class); got != want {
			t.Errorf("IsAutomated(%q) = %v, want %v", class, got, want)
		}
	}
}

// benchHeaders is a realistic ~40-header inbound reply (the inbox poll processes
// up to 200 of these per pass, so per-reply allocations matter).
func benchHeaders() map[string][]string {
	return map[string][]string{
		"Return-Path":                    {"<sender@example.com>"},
		"Delivered-To":                   {"rep@ourco.com"},
		"Received":                       {"from mx.example.com by mx.ourco.com"},
		"Received-Spf":                   {"pass"},
		"Authentication-Results":         {"spf=pass; dkim=pass; dmarc=pass"},
		"Dkim-Signature":                 {"v=1; a=rsa-sha256; c=relaxed/relaxed"},
		"From":                           {"Jane Prospect <jane@example.com>"},
		"To":                             {"rep@ourco.com"},
		"Subject":                        {"Re: quick question about your platform"},
		"Message-Id":                     {"<abc123@example.com>"},
		"In-Reply-To":                    {"<our-send@ourco.com>"},
		"References":                     {"<our-send@ourco.com>"},
		"Date":                           {"Fri, 25 Jul 2026 10:00:00 +0000"},
		"Mime-Version":                   {"1.0"},
		"Content-Type":                   {"text/plain; charset=UTF-8"},
		"Content-Transfer-Encoding":      {"quoted-printable"},
		"X-Mailer":                       {"Apple Mail (2.3696.120.41.1.1)"},
		"X-Originating-Ip":               {"[203.0.113.7]"},
		"X-Spam-Score":                   {"-1.2"},
		"X-Spam-Status":                  {"No"},
		"Thread-Topic":                   {"quick question about your platform"},
		"Thread-Index":                   {"AQHZ..."},
		"Accept-Language":                {"en-US"},
		"Content-Language":               {"en-US"},
		"X-Ms-Exchange-Organization-Scl": {"-1"},
		"X-Ms-Has-Attach":                {""},
		"X-Ms-Tnef-Correlator":           {""},
		"X-Ms-Exchange-Transport-Fromentityheader": {"Hosted"},
		"X-Google-Smtp-Source":                     {"AGHT+abc"},
		"X-Received":                               {"by 2002:..."},
		"Arc-Seal":                                 {"i=1; a=rsa-sha256"},
		"Arc-Message-Signature":                    {"i=1; a=rsa-sha256"},
		"Arc-Authentication-Results":               {"i=1; spf=pass"},
		"List-Id":                                  {""},
		"Reply-To":                                 {"jane@example.com"},
		"Sender":                                   {"jane@example.com"},
		"X-Entity-Ref-Id":                          {"abc"},
		"Precedence":                               {""},
		"Auto-Submitted":                           {""},
		"X-Priority":                               {"3"},
	}
}

const benchBody = "Thanks for reaching out. I took a look at the deck you sent over and " +
	"have a few questions about how the warmup pool is seeded and what the ramp " +
	"schedule looks like for a brand new domain. We are evaluating a couple of " +
	"options right now and timing matters, so a short call next week could help " +
	"us move faster. Let me know what works on your end and I will get it on the calendar."

func BenchmarkClassifyHeaders(b *testing.B) {
	cls := New(nil)
	in := Input{Headers: benchHeaders(), Subject: "Re: quick question", BodyText: "short reply"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cls.Classify(context.Background(), in)
	}
}

func BenchmarkClassifyLexicon(b *testing.B) {
	cls := New(nil)
	in := Input{Subject: "Re: quick question about your platform", BodyText: benchBody}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cls.Classify(context.Background(), in)
	}
}

func BenchmarkClassifyFull(b *testing.B) {
	cls := New(nil)
	in := Input{Headers: benchHeaders(), Subject: "Re: quick question about your platform", BodyText: benchBody}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cls.Classify(context.Background(), in)
	}
}

// TestLooksLikeUnsubscribe locks the compliance-only helper: it fires on the
// same opt-out tokens Layer 2 uses (subject or body), stays boundary-aware
// ("nonstop"/"stopped by" do not trip "stop"), and ignores non-opt-out text —
// so a caller can honor an opt-out even inside an otherwise-automated message.
func TestLooksLikeUnsubscribe(t *testing.T) {
	cls := New(nil)
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"body unsubscribe", Input{Subject: "Re: hi", BodyText: "please unsubscribe me"}, true},
		{"remove me", Input{Subject: "Re: hi", BodyText: "remove me from your list"}, true},
		{"stop boundary token", Input{Subject: "Re: hi", BodyText: "please stop"}, true},
		{"opt-out inside OOO subject", Input{Subject: "Out of Office", BodyText: "away, but take me off your list"}, true},
		{"nonstop is not stop (boundary)", Input{Subject: "Re: hi", BodyText: "we work nonstop and stopped by earlier"}, false},
		{"plain positive is not opt-out", Input{Subject: "Re: hi", BodyText: "sounds great, let's chat"}, false},
		{"empty", Input{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cls.LooksLikeUnsubscribe(tc.in); got != tc.want {
				t.Fatalf("LooksLikeUnsubscribe(%q/%q) = %v, want %v", tc.in.Subject, tc.in.BodyText, got, tc.want)
			}
		})
	}
}
