package mail

import (
	"context"
	"reflect"
	"testing"

	gmail "google.golang.org/api/gmail/v1"
)

func TestGmailMsgIDQueryStripsAngleBrackets(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"<abc@x.com>", "rfc822msgid:abc@x.com"},
		{"abc@x.com", "rfc822msgid:abc@x.com"},
		{"<only-left@x", "rfc822msgid:only-left@x"},
		{"", "rfc822msgid:"},
	}
	for _, tc := range cases {
		if got := gmailMsgIDQuery(tc.in); got != tc.want {
			t.Errorf("gmailMsgIDQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// gmailEngageRecorder captures the label modify(s) a run performed.
type gmailEngageRecorder struct {
	foundMessageID string
	modifiedIDs    []string
	adds           [][]string
	removes        [][]string
}

func newFakeGmailEngager(rec *gmailEngageRecorder, matches []string) *GmailEngager {
	return &GmailEngager{
		newServiceFn: func(context.Context, string) (*gmail.Service, error) { return nil, nil },
		findFn: func(_ context.Context, _ *gmail.Service, messageID string) ([]string, error) {
			rec.foundMessageID = messageID
			return matches, nil
		},
		modifyFn: func(_ context.Context, _ *gmail.Service, msgID string, add, remove []string) error {
			rec.modifiedIDs = append(rec.modifiedIDs, msgID)
			rec.adds = append(rec.adds, add)
			rec.removes = append(rec.removes, remove)
			return nil
		},
	}
}

func TestGmailEngagerMarkReadRemovesUnread(t *testing.T) {
	rec := &gmailEngageRecorder{}
	e := newFakeGmailEngager(rec, []string{"m1"})

	if err := e.MarkRead(context.Background(), EngageTarget{Provider: "gmail", AccessToken: []byte("tok"), MessageID: "<a@b>"}); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if rec.foundMessageID != "<a@b>" {
		t.Fatalf("find got %q, want the raw Message-ID <a@b>", rec.foundMessageID)
	}
	if !reflect.DeepEqual(rec.modifiedIDs, []string{"m1"}) {
		t.Fatalf("modified ids = %v, want [m1]", rec.modifiedIDs)
	}
	if !reflect.DeepEqual(rec.removes[0], []string{gmailLabelUnread}) || len(rec.adds[0]) != 0 {
		t.Fatalf("mark-read labels: add=%v remove=%v, want add [] remove [UNREAD]", rec.adds[0], rec.removes[0])
	}
}

func TestGmailEngagerRescueRemovesSpamAddsInbox(t *testing.T) {
	rec := &gmailEngageRecorder{}
	e := newFakeGmailEngager(rec, []string{"m1"})

	if err := e.Rescue(context.Background(), EngageTarget{Provider: "gmail", AccessToken: []byte("tok"), MessageID: "<a@b>"}); err != nil {
		t.Fatalf("Rescue: %v", err)
	}
	if !reflect.DeepEqual(rec.adds[0], []string{gmailLabelInbox}) || !reflect.DeepEqual(rec.removes[0], []string{gmailLabelSpam}) {
		t.Fatalf("rescue labels: add=%v remove=%v, want add [INBOX] remove [SPAM]", rec.adds[0], rec.removes[0])
	}
}

func TestGmailEngagerNoMatchIsNoop(t *testing.T) {
	rec := &gmailEngageRecorder{}
	e := newFakeGmailEngager(rec, nil) // rfc822msgid resolved to zero messages

	if err := e.MarkRead(context.Background(), EngageTarget{Provider: "gmail", MessageID: "<gone@b>"}); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if len(rec.modifiedIDs) != 0 {
		t.Fatalf("modify called on a no-match: %v", rec.modifiedIDs)
	}
}
