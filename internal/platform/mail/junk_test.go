package mail

import (
	"testing"

	"github.com/emersion/go-imap"
)

// TestPickJunkFolder covers the best-effort junk-folder resolution: the RFC 6154
// SPECIAL-USE \Junk attribute wins regardless of the folder's name, a \Noselect
// placeholder is never chosen, and — absent a special-use flag — the first
// case-insensitive common-name match is used. No match yields ok=false, which the
// poller treats as "scan INBOX only".
func TestPickJunkFolder(t *testing.T) {
	info := func(name string, attrs ...string) *imap.MailboxInfo {
		return &imap.MailboxInfo{Name: name, Attributes: attrs}
	}

	cases := []struct {
		name     string
		infos    []*imap.MailboxInfo
		wantName string
		wantOK   bool
	}{
		{
			name:     "special-use junk wins over name",
			infos:    []*imap.MailboxInfo{info("INBOX"), info("Spambucket", imap.JunkAttr)},
			wantName: "Spambucket",
			wantOK:   true,
		},
		{
			name:     "common name fallback (case-insensitive)",
			infos:    []*imap.MailboxInfo{info("INBOX"), info("jUnK")},
			wantName: "jUnK",
			wantOK:   true,
		},
		{
			name:     "gmail-style spam path",
			infos:    []*imap.MailboxInfo{info("INBOX"), info("[Gmail]/Spam")},
			wantName: "[Gmail]/Spam",
			wantOK:   true,
		},
		{
			name:     "noselect junk is skipped",
			infos:    []*imap.MailboxInfo{info("Junk", imap.JunkAttr, imap.NoSelectAttr)},
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "special-use preferred over an earlier common name",
			infos:    []*imap.MailboxInfo{info("Spam"), info("Papierkorb", imap.JunkAttr)},
			wantName: "Papierkorb",
			wantOK:   true,
		},
		{
			name:     "no junk folder",
			infos:    []*imap.MailboxInfo{info("INBOX"), info("Sent"), info("Archive")},
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "nil entries tolerated",
			infos:    []*imap.MailboxInfo{nil, info("Junk")},
			wantName: "Junk",
			wantOK:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickJunkFolder(tc.infos)
			if got != tc.wantName || ok != tc.wantOK {
				t.Errorf("pickJunkFolder = (%q, %v), want (%q, %v)", got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}
