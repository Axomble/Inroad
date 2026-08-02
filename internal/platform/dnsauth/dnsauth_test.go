package dnsauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

// fakeResolver answers from a fixed map: name -> TXT records, or name -> error.
// Nothing in this file touches real DNS.
type fakeResolver struct {
	txt  map[string][]string
	errs map[string]error
	// calls records lookup order so a test can assert the probe stopped early.
	calls []string
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if txt, ok := f.txt[name]; ok {
		return txt, nil
	}
	return nil, notFound(name)
}

// notFound is what Go's resolver returns for NXDOMAIN/NODATA: a *net.DNSError
// with IsNotFound set. That flag — not the message — is what the checker reads.
func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// transient is a resolver failure that says nothing about the domain: a timeout.
// IsNotFound is false, which is the whole distinction invariant 1 rests on.
func transient(name string) error {
	return &net.DNSError{Err: "i/o timeout", Name: name, IsTimeout: true, IsTemporary: true}
}

const domain = "acme.com"

var (
	spfRecord   = "v=spf1 include:_spf.google.com ~all"
	dmarcRecord = "v=DMARC1; p=reject; rua=mailto:dmarc@acme.com"
	dkimRecord  = "v=DKIM1; k=rsa; p=MIGfMA0GCSq"
)

func TestCheckStateMatrix(t *testing.T) {
	tests := []struct {
		name  string
		txt   map[string][]string
		errs  map[string]error
		want  State
		spf   bool
		dmarc bool
		dkim  bool
	}{
		{
			name:  "spf and dmarc both published",
			txt:   map[string][]string{domain: {spfRecord}, "_dmarc." + domain: {dmarcRecord}},
			want:  StatePassing,
			spf:   true,
			dmarc: true,
		},
		{
			name: "spf only",
			txt:  map[string][]string{domain: {spfRecord}},
			want: StateFailing,
			spf:  true,
		},
		{
			name:  "dmarc only",
			txt:   map[string][]string{"_dmarc." + domain: {dmarcRecord}},
			want:  StateFailing,
			dmarc: true,
		},
		{
			name: "neither",
			want: StateFailing,
		},
		{
			// Both authoritative records present AND a DKIM key: still passing,
			// and nothing about the verdict came from DKIM.
			name: "dkim present changes nothing when spf and dmarc pass",
			txt: map[string][]string{
				domain:                              {spfRecord},
				"_dmarc." + domain:                  {dmarcRecord},
				"google._domainkey." + domain:       {dkimRecord},
				"selector1._domainkey." + domain:    {dkimRecord},
				"somewhere._domainkey." + domain:    {dkimRecord},
				"_dmarc.somewhere." + domain:        {dmarcRecord},
				"unused._domainkey.somewhere.local": {dkimRecord},
			},
			want:  StatePassing,
			spf:   true,
			dmarc: true,
			dkim:  true,
		},
		{
			// The judgement that makes this feature honest: a domain signing on
			// a selector we never guess is NOT reported as broken.
			name:  "dkim absent changes nothing when spf and dmarc pass",
			txt:   map[string][]string{domain: {spfRecord}, "_dmarc." + domain: {dmarcRecord}},
			want:  StatePassing,
			spf:   true,
			dmarc: true,
		},
		{
			name: "dkim present cannot rescue a domain with no spf or dmarc",
			txt:  map[string][]string{"google._domainkey." + domain: {dkimRecord}},
			want: StateFailing,
			dkim: true,
		},
		{
			// NXDOMAIN is a real negative: the record genuinely is not there.
			name: "nxdomain on both is failing, not unknown",
			errs: map[string]error{domain: notFound(domain), "_dmarc." + domain: notFound("_dmarc." + domain)},
			want: StateFailing,
		},
		{
			name:  "transient error on spf is unknown even though dmarc is present",
			txt:   map[string][]string{"_dmarc." + domain: {dmarcRecord}},
			errs:  map[string]error{domain: transient(domain)},
			want:  StateUnknown,
			dmarc: true,
		},
		{
			name: "transient error on dmarc is unknown even though spf is present",
			txt:  map[string][]string{domain: {spfRecord}},
			errs: map[string]error{"_dmarc." + domain: transient("_dmarc." + domain)},
			want: StateUnknown,
			spf:  true,
		},
		{
			// A DKIM probe failing is not an authoritative failure: the verdict
			// stands on SPF+DMARC alone.
			name: "transient error on a dkim probe never yields unknown",
			txt:  map[string][]string{domain: {spfRecord}, "_dmarc." + domain: {dmarcRecord}},
			errs: map[string]error{
				"google._domainkey." + domain:    transient("google._domainkey." + domain),
				"default._domainkey." + domain:   transient("default._domainkey." + domain),
				"selector1._domainkey." + domain: transient("selector1._domainkey." + domain),
			},
			want:  StatePassing,
			spf:   true,
			dmarc: true,
		},
		{
			// A name carries many TXT records; the ones that are not ours are
			// ignored rather than treated as a malformed answer.
			name: "unrelated txt records are ignored",
			txt: map[string][]string{
				domain:             {"google-site-verification=abc", "MS=ms12345", spfRecord},
				"_dmarc." + domain: {"some-other-vendor=1", dmarcRecord},
			},
			want:  StatePassing,
			spf:   true,
			dmarc: true,
		},
		{
			name: "records that only look like ours are not matched",
			txt: map[string][]string{
				domain:             {"spf1 include:example.com", "v=spf10 nope"},
				"_dmarc." + domain: {"v=DMARC2; p=reject"},
			},
			want: StateFailing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &fakeResolver{txt: tt.txt, errs: tt.errs}
			got := Check(context.Background(), res, domain, nil)
			if got.State() != tt.want {
				t.Fatalf("State() = %q, want %q (spf=%t dmarc=%t lookup_error=%t)",
					got.State(), tt.want, got.SPF.Found, got.DMARC.Found, got.LookupError)
			}
			if got.SPF.Found != tt.spf {
				t.Errorf("SPF.Found = %t, want %t", got.SPF.Found, tt.spf)
			}
			if got.DMARC.Found != tt.dmarc {
				t.Errorf("DMARC.Found = %t, want %t", got.DMARC.Found, tt.dmarc)
			}
			if got.DKIM.Found != tt.dkim {
				t.Errorf("DKIM.Found = %t, want %t", got.DKIM.Found, tt.dkim)
			}
		})
	}
}

// A transient failure must never be reported as a misconfiguration, and NXDOMAIN
// must never be softened into "we couldn't check". This asserts the distinction
// directly on the flag the checker reads, so it cannot regress into a string match.
func TestLookupErrorDistinguishesNotFoundFromTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"nxdomain", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, true},
		{"servfail", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, true},
		// errors.As unwraps, so a nested NXDOMAIN is still a real negative.
		{"wrapped nxdomain", fmt.Errorf("lookup: %w", &net.DNSError{Err: "no such host", IsNotFound: true}), false},
		{"non-dns error", errors.New("resolver unavailable"), true},
		{"context cancelled", context.Canceled, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLookupError(tt.err); got != tt.want {
				t.Fatalf("isLookupError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestSelectorProbeStopsAtFirstHit(t *testing.T) {
	res := &fakeResolver{txt: map[string][]string{
		domain:                           {spfRecord},
		"_dmarc." + domain:               {dmarcRecord},
		"selector1._domainkey." + domain: {dkimRecord},
		"selector2._domainkey." + domain: {dkimRecord},
	}}
	got := Check(context.Background(), res, domain, nil)
	if !got.DKIM.Found || got.DKIM.Selector != "selector1" {
		t.Fatalf("DKIM = %+v, want found on selector1", got.DKIM)
	}
	for _, name := range res.calls {
		if name == "selector2._domainkey."+domain {
			t.Fatalf("probe continued past the first hit: %v", res.calls)
		}
	}
}

func TestOperatorSuppliedSelectorIsProbedAfterTheDefaults(t *testing.T) {
	res := &fakeResolver{txt: map[string][]string{
		"custom2024._domainkey." + domain: {dkimRecord},
	}}
	got := Check(context.Background(), res, domain, []string{"", "custom2024"})
	if !got.DKIM.Found || got.DKIM.Selector != "custom2024" {
		t.Fatalf("DKIM = %+v, want found on custom2024", got.DKIM)
	}
}

func TestDMARCPolicyParsedFromAnyTagPosition(t *testing.T) {
	tests := []struct {
		record string
		want   string
	}{
		{"v=DMARC1; p=none", "none"},
		{"v=DMARC1; p=quarantine; pct=50", "quarantine"},
		{"v=DMARC1; rua=mailto:a@b.c; adkim=s; p=reject; aspf=s", "reject"},
		{"v=DMARC1;p=reject", "reject"},
		{"v=DMARC1 ;  P = Reject ; sp=none", "reject"},
		// sp= is the SUBDOMAIN policy; reading it as the domain's policy would
		// misreport a domain that monitors itself and enforces on children.
		{"v=DMARC1; sp=reject", ""},
		{"v=DMARC1; rua=mailto:a@b.c", ""},
		{"v=DMARC1; p=bogus", ""},
	}
	for _, tt := range tests {
		t.Run(tt.record, func(t *testing.T) {
			res := &fakeResolver{txt: map[string][]string{"_dmarc." + domain: {tt.record}}}
			got := Check(context.Background(), res, domain, nil)
			if !got.DMARC.Found {
				t.Fatalf("DMARC.Found = false for %q", tt.record)
			}
			if got.DMARC.Policy != tt.want {
				t.Fatalf("Policy = %q, want %q", got.DMARC.Policy, tt.want)
			}
		})
	}
}

func TestDKIMRecordWithoutVersionTagStillCounts(t *testing.T) {
	// RFC 6376 makes v=DKIM1 recommended, not required; live records often
	// start at k=/p=. Under-detecting here would tell a signing operator they
	// are not signing.
	res := &fakeResolver{txt: map[string][]string{
		"default._domainkey." + domain: {"k=rsa; p=MIGfMA0GCSqGSIb3"},
	}}
	got := Check(context.Background(), res, domain, nil)
	if !got.DKIM.Found || got.DKIM.Selector != "default" {
		t.Fatalf("DKIM = %+v, want found on default", got.DKIM)
	}
}

func TestRevokedDKIMKeyIsNotAMatch(t *testing.T) {
	// An empty p= is a REVOKED key (RFC 6376 §3.6.1), not a published one.
	res := &fakeResolver{txt: map[string][]string{
		"google._domainkey." + domain: {"k=rsa; p="},
	}}
	if got := Check(context.Background(), res, domain, nil); got.DKIM.Found {
		t.Fatalf("DKIM = %+v, want not found for a revoked key", got.DKIM)
	}
}

func TestNormalizeCollapsesEquivalentSpellings(t *testing.T) {
	for _, in := range []string{"ACME.com", " acme.com ", "acme.com.", "Acme.COM."} {
		if got := Normalize(in); got != domain {
			t.Fatalf("Normalize(%q) = %q, want %q", in, got, domain)
		}
	}
	res := &fakeResolver{txt: map[string][]string{domain: {spfRecord}, "_dmarc." + domain: {dmarcRecord}}}
	got := Check(context.Background(), res, "ACME.com.", nil)
	if got.Domain != domain || got.State() != StatePassing {
		t.Fatalf("Check(%q) = domain %q state %q, want %q passing", "ACME.com.", got.Domain, got.State(), domain)
	}
}

func TestEmptyDomainIsUnknownAndLooksUpNothing(t *testing.T) {
	res := &fakeResolver{}
	got := Check(context.Background(), res, "  ", nil)
	if got.State() != StateUnknown {
		t.Fatalf("State() = %q, want %q", got.State(), StateUnknown)
	}
	if len(res.calls) != 0 {
		t.Fatalf("lookups performed for an empty domain: %v", res.calls)
	}
}

// A domain that cannot be a hostname must normalize to "", which every caller
// already treats as "not ours". Before this, a path parameter carrying a control
// character reached Postgres, which rejected it as an invalid text literal — an
// error class the handler does not map, so bad input became a 500. QA found it
// with %00; the shape check closes the whole class at the single choke point.
func TestNormalizeRejectsAnythingThatCannotBeAHostname(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"embedded NUL", "acme.test\x00.google.com"},
		{"leading NUL", "\x00acme.test"},
		{"newline inside", "acme\n.test"},
		{"tab inside", "acme\t.test"},
		{"bell", "acme.test\a"},
		{"path traversal", "acme.test/../google.com"},
		{"backslash", `acme.test\google.com`},
		{"space inside", "acme test"},
		{"raw non-ascii", "acme.tést"},
		{"fullwidth stop", "acme。test"},
		{"empty", ""},
		{"only a dot", "."},
		{"only whitespace", "   "},
		{"over the DNS length limit", strings.Repeat("a", 254)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != "" {
				t.Errorf("Normalize(%q) = %q, want \"\" — a caller must never pass this on to a query or a resolver", tc.in, got)
			}
		})
	}
}

// The shape check must not reject names people legitimately send from.
func TestNormalizeKeepsRealDomains(t *testing.T) {
	cases := map[string]string{
		"ACME.com.":               "acme.com",
		"  mail.acme.co.uk  ":     "mail.acme.co.uk",
		"xn--80ak6aa92e.com":      "xn--80ak6aa92e.com", // punycode IDN
		"my-domain.example":       "my-domain.example",
		"_dmarc.acme.com":         "_dmarc.acme.com",
		"a.b.c.d.e.f.g.h.example": "a.b.c.d.e.f.g.h.example",
		strings.Repeat("a", 253):  strings.Repeat("a", 253), // exactly at the limit
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
