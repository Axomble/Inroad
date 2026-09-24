package mail

import (
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

// The junk folder is the one mailbox name IMAP genuinely does localize, and
// resolving it wrong costs the whole spam-placement signal warmup exists to
// measure — silently, because ErrNoJunkFolder is a non-fatal "scan INBOX only".
//
// (INBOX itself is not in this category: RFC 3501 §5.1 reserves the name INBOX,
// case-insensitively, on every compliant server. RFC 6154 defines \All,
// \Archive, \Drafts, \Flagged, \Junk, \Sent and \Trash — and no inbox
// attribute at all — so special-use is not an alternative way to find it.)

func TestPickJunkFolderResolvesLocalizedNames(t *testing.T) {
	info := func(name string, attrs ...string) *imap.MailboxInfo {
		return &imap.MailboxInfo{Name: name, Attributes: attrs}
	}
	for _, tc := range []struct {
		locale, folder string
	}{
		{"German", "Spamverdacht"},
		{"German (Outlook)", "Junk-E-Mail"},
		{"French", "Courrier indésirable"},
		{"French (short)", "Indésirables"},
		{"French (Pourriel)", "Pourriel"},
		{"Spanish", "Correo no deseado"},
		{"Portuguese", "Lixo Eletrônico"},
		{"Italian", "Posta indesiderata"},
		{"Dutch", "Ongewenste e-mail"},
		{"Swedish", "Skräppost"},
		{"Danish", "Uønsket mail"},
		{"Finnish", "Roskaposti"},
		{"Czech", "Nevyžádaná pošta"},
		{"Japanese", "迷惑メール"},
		{"Chinese", "垃圾邮件"},
		{"Norwegian", "Søppelpost"},
		{"Polish", "Wiadomości-śmieci"},
		{"Russian", "Спам"},
		{"Turkish", "İstenmeyen"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			infos := []*imap.MailboxInfo{info("INBOX"), info("Sent"), info(tc.folder)}
			got, ok := pickJunkFolder(infos)
			if !ok || got != tc.folder {
				t.Errorf("pickJunkFolder = (%q, %v), want (%q, true)", got, ok, tc.folder)
			}
		})
	}
}

// Localization must not widen the match into something destructive. "Trash",
// "Deleted Items" and "Archive" are not junk folders, and rescuing a warmup
// message out of one would move real mail.
func TestPickJunkFolderIgnoresNonJunkFolders(t *testing.T) {
	info := func(name string) *imap.MailboxInfo { return &imap.MailboxInfo{Name: name} }
	for _, name := range []string{
		"Trash", "Deleted Items", "Papierkorb", "Corbeille",
		"Archive", "Sent Items", "Drafts", "Notes",
	} {
		if got, ok := pickJunkFolder([]*imap.MailboxInfo{info("INBOX"), info(name)}); ok {
			t.Errorf("%q was resolved as the junk folder (%q)", name, got)
		}
	}
}

// End to end through the real reader: a German Outlook-style server whose junk
// folder carries no special-use flag at all, which is the case the name list is
// the only answer for.
func TestFetchJunkResolvesALocalizedFolderOverTheWire(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		User: "u", Pass: "p",
		Mailboxes: []imapMailbox{
			{Name: "INBOX", Attrs: []string{"\\HasNoChildren"}},
			{Name: "Gesendete Elemente", Attrs: []string{"\\HasNoChildren"}},
			{Name: "Junk-E-Mail", Attrs: []string{"\\HasNoChildren"}},
		},
		Messages: []imapFakeMessage{{UID: 4, Raw: "Subject: spam\n\nbody\n"}},
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	msgs, folder, err := r.FetchJunk(t.Context(), fakeIMAPConfig("u", "p"), 10)
	if err != nil {
		t.Fatalf("FetchJunk: %v", err)
	}
	if folder != "Junk-E-Mail" {
		t.Errorf("resolved folder %q, want %q", folder, "Junk-E-Mail")
	}
	if srv.selectedMailbox() != "Junk-E-Mail" {
		t.Errorf("SELECTed %q, want %q", srv.selectedMailbox(), "Junk-E-Mail")
	}
	if len(msgs) != 1 || msgs[0].UID != 4 {
		t.Errorf("got %d messages, want 1 with UID 4", len(msgs))
	}
}

// The special-use flag still wins over any name, in any language.
func TestFetchJunkPrefersSpecialUseOverTheNameList(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		User: "u", Pass: "p",
		Mailboxes: []imapMailbox{
			{Name: "INBOX"},
			{Name: "Spam", Attrs: []string{"\\HasNoChildren"}},
			{Name: "Quarantäne", Attrs: []string{"\\Junk"}},
		},
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	_, folder, err := r.FetchJunk(t.Context(), fakeIMAPConfig("u", "p"), 10)
	if err != nil {
		t.Fatalf("FetchJunk: %v", err)
	}
	if folder != "Quarantäne" {
		t.Errorf("resolved %q, want the \\Junk folder %q", folder, "Quarantäne")
	}
	if srv.selectedMailbox() != "Quarantäne" {
		t.Errorf("SELECTed %q", srv.selectedMailbox())
	}
}

// A server with no junk folder at all is a clean non-fatal signal, not a poll
// failure — the poller logs it and scans INBOX only.
func TestFetchJunkReportsNoJunkFolder(t *testing.T) {
	startFakeIMAP(t, imapScript{
		User: "u", Pass: "p",
		Mailboxes: []imapMailbox{{Name: "INBOX"}, {Name: "Sent"}},
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.FetchJunk(t.Context(), fakeIMAPConfig("u", "p"), 10); !errors.Is(err, ErrNoJunkFolder) {
		t.Fatalf("got %v, want ErrNoJunkFolder", err)
	}
}
