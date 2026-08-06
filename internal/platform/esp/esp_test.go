package esp

import (
	"context"
	"errors"
	"net"
	"testing"
)

// The transport tag alone is not the answer (judgement 1): the cases that matter
// are the Workspace and 365 mailboxes connected over plain SMTP, which provider
// reports as 'smtp'.
func TestFromMailbox(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		smtpHost string
		want     ESP
	}{
		{"gmail api transport", "gmail", "", Google},
		{"m365 api transport", "m365", "", Microsoft},
		{"api transport wins over an unrelated host", "gmail", "smtp.mailgun.org", Google},
		{"workspace over app password", "smtp", "smtp.gmail.com", Google},
		{"workspace relay", "smtp", "smtp-relay.gmail.com", Google},
		{"365 over basic auth", "smtp", "smtp.office365.com", Microsoft},
		{"365 alternate submission host", "smtp", "outlook.office365.com", Microsoft},
		{"host is case- and dot-insensitive", "smtp", " SMTP.Gmail.Com. ", Google},
		{"relay-fronted tenant reads other", "smtp", "smtp.sendgrid.net", Other},
		{"self-hosted postfix reads other", "smtp", "mail.acme.com", Other},
		{"near-miss suffix must not match", "smtp", "smtp.gmail.com.evil.example", Other},
		{"prefix-glued near-miss must not match", "smtp", "notsmtp.gmail.com", Other},
		{"misconfigured smtp mailbox with no host", "smtp", "", Other},
		{"unrecognised transport falls through to the host", "", "smtp.gmail.com", Google},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromMailbox(tc.provider, tc.smtpHost); got != tc.want {
				t.Errorf("FromMailbox(%q, %q) = %q, want %q", tc.provider, tc.smtpHost, got, tc.want)
			}
		})
	}
}

// FromMailbox must never return Unknown: it reads columns already in hand, so
// the classification always completes (judgement 3). A mailbox that read Unknown
// would be re-checked forever by a sweep that has nothing to look up.
func TestFromMailboxIsNeverUnknown(t *testing.T) {
	for _, provider := range []string{"smtp", "gmail", "m365", "", "nonsense"} {
		for _, host := range []string{"", " ", "smtp.gmail.com", "mail.acme.com", "..."} {
			if got := FromMailbox(provider, host); got == Unknown {
				t.Errorf("FromMailbox(%q, %q) = unknown", provider, host)
			}
		}
	}
}

func TestFromMX(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hosts []string
		want  ESP
	}{
		{"workspace primary", []string{"aspmx.l.google.com"}, Google},
		{"workspace full record set", []string{
			"aspmx.l.google.com", "alt1.aspmx.l.google.com", "aspmx2.googlemail.com",
		}, Google},
		{"consumer gmail", []string{"gmail-smtp-in.l.google.com"}, Google},
		{"365 tenant", []string{"acme-com.mail.protection.outlook.com"}, Microsoft},
		{"consumer outlook", []string{"eur.olc.protection.outlook.com"}, Microsoft},
		{"hotmail", []string{"mx1.hotmail.com"}, Microsoft},
		{"trailing root dot from a real answer", []string{"ASPMX.L.GOOGLE.COM."}, Google},
		{"third-party filter", []string{"mx1.mailgun.org"}, Other},
		{"near-miss suffix must not match", []string{"mx.google.com.evil.example"}, Other},
		{"prefix-glued near-miss must not match", []string{"notgoogle.com"}, Other},
		{"no mx at all is unknown, not other", nil, Unknown},
		{"empty host strings are skipped, not classified", []string{"", "  "}, Other},
		// Preference order is load-bearing: a Google domain with a third-party
		// backup MX must read Google, and reading the backup would invert it.
		{"primary wins over a third-party backup", []string{
			"aspmx.l.google.com", "backup.mailgun.org",
		}, Google},
		{"third-party primary in front of google reads that filter", []string{
			"mx.proofpoint.example", "aspmx.l.google.com",
		}, Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromMX(tc.hosts); got != tc.want {
				t.Errorf("FromMX(%v) = %q, want %q", tc.hosts, got, tc.want)
			}
		})
	}
}

// Only Google and Microsoft pair. Other is a catch-all bucket, so two Others
// matching would be a coincidence presented as a decision (judgement 2).
func TestMatchable(t *testing.T) {
	for e, want := range map[ESP]bool{Google: true, Microsoft: true, Other: false, Unknown: false} {
		if got := e.Matchable(); got != want {
			t.Errorf("%q.Matchable() = %v, want %v", e, got, want)
		}
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"google": true, "microsoft": true, "other": true, "unknown": true,
		"": false, "Google": false, "gmail": false, "m365": false,
	} {
		if got := Valid(s); got != want {
			t.Errorf("Valid(%q) = %v, want %v", s, got, want)
		}
	}
}

// fakeResolver answers MX from a table so no test touches real DNS.
type fakeResolver struct {
	mx    map[string][]*net.MX
	errs  map[string]error
	calls []string
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	return f.mx[name], nil
}

func mx(hosts ...string) []*net.MX {
	out := make([]*net.MX, len(hosts))
	for i, h := range hosts {
		out[i] = &net.MX{Host: h, Pref: uint16(10 + i)}
	}
	return out
}

// The ok flag is the whole point of Lookup: a transient resolver failure must be
// distinguishable from a real negative, because only the latter may be
// persisted (persisting the former would stamp checked_at and hide the domain
// from the sweep for a full staleness window).
func TestLookup(t *testing.T) {
	res := &fakeResolver{
		mx: map[string][]*net.MX{
			"acme.com":     mx("aspmx.l.google.com", "alt1.aspmx.l.google.com"),
			"contoso.com":  mx("contoso-com.mail.protection.outlook.com"),
			"relayed.com":  mx("mx.mailgun.org"),
			"nomx.com":     nil,
			"nilrecord.io": {nil, {Host: "aspmx.l.google.com"}},
		},
		errs: map[string]error{
			"gone.com":    &net.DNSError{Err: "no such host", IsNotFound: true},
			"timeout.com": &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			"broken.com":  errors.New("resolver exploded"),
		},
	}
	for _, tc := range []struct {
		name     string
		domain   string
		want     ESP
		wantHost string
		wantOK   bool
	}{
		{"google domain", "acme.com", Google, "aspmx.l.google.com", true},
		{"microsoft domain", "contoso.com", Microsoft, "contoso-com.mail.protection.outlook.com", true},
		{"relayed domain", "relayed.com", Other, "mx.mailgun.org", true},
		{"no mx records is a completed negative", "nomx.com", Other, "", true},
		{"nxdomain is a completed negative", "gone.com", Other, "", true},
		{"nil records are skipped", "nilrecord.io", Google, "aspmx.l.google.com", true},
		{"timeout is not an answer", "timeout.com", Unknown, "", false},
		{"opaque resolver error is not an answer", "broken.com", Unknown, "", false},
		{"empty domain is never looked up", "", Unknown, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, host, ok := Lookup(t.Context(), res, tc.domain)
			if got != tc.want || host != tc.wantHost || ok != tc.wantOK {
				t.Errorf("Lookup(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.domain, got, host, ok, tc.want, tc.wantHost, tc.wantOK)
			}
		})
	}
	for _, c := range res.calls {
		if c == "" {
			t.Error("Lookup queried the resolver for an empty domain")
		}
	}
}
