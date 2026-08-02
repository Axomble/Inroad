//go:build integration

package sendingdomain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
)

func connect(t *testing.T) *gen.Queries {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return gen.New(pool)
}

func seedWorkspace(t *testing.T, ctx context.Context, q *gen.Queries) uuid.UUID {
	t.Helper()
	ws, err := q.CreateWorkspace(ctx, "SendingDomain IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws.ID
}

func seedMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, email string) {
	t.Helper()
	if _, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: "IT",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	}); err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
}

// The whole domain list is a projection of mailboxes.email: connecting a mailbox
// is the only way a domain appears, and ten mailboxes on one domain are one row.
func TestListDerivesDomainsFromMailboxes(t *testing.T) {
	ctx := context.Background()
	q := connect(t)
	store := NewPgStore(q)

	ws := seedWorkspace(t, ctx, q)
	suffix := uuid.NewString()[:8]
	shared, solo := "shared-"+suffix+".test", "solo-"+suffix+".test"
	seedMailbox(t, ctx, q, ws, "a@"+shared)
	// Mixed case in the stored address must not fork the domain into two rows.
	seedMailbox(t, ctx, q, ws, "b@"+strings.ToUpper(shared))
	seedMailbox(t, ctx, q, ws, "c@"+solo)

	got, err := store.List(ctx, ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %+v, want 2 domains", got)
	}
	byName := map[string]Domain{got[0].Domain: got[0], got[1].Domain: got[1]}
	if d := byName[shared]; d.MailboxCount != 2 || d.State != dnsauth.StateUnknown || d.CheckedAt != nil {
		t.Fatalf("%s = %+v, want 2 mailboxes, unknown, never checked", shared, d)
	}
	if d := byName[solo]; d.MailboxCount != 1 {
		t.Fatalf("%s = %+v, want 1 mailbox", solo, d)
	}

	// A completed check surfaces on the derived row without changing the count.
	res := dnsauth.Result{
		Domain: shared,
		SPF:    dnsauth.SPF{Found: true, Value: "v=spf1 -all"},
		DMARC:  dnsauth.DMARC{Found: true, Value: "v=DMARC1; p=none", Policy: "none"},
		DKIM:   dnsauth.DKIM{Found: true, Value: "v=DKIM1; p=x", Selector: "google"},
	}
	checkedAt, err := store.Record(ctx, ws, res)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	after, err := store.Get(ctx, ws, shared)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.State != dnsauth.StatePassing || after.MailboxCount != 2 || after.DMARCPolicy != "none" {
		t.Fatalf("after check = %+v, want passing with 2 mailboxes and p=none", after)
	}
	if after.CheckedAt == nil || !after.CheckedAt.Equal(checkedAt) {
		t.Fatalf("CheckedAt = %v, want %v", after.CheckedAt, checkedAt)
	}
	if after.DKIMSelector != "google" {
		t.Fatalf("DKIMSelector = %q, want google", after.DKIMSelector)
	}
}

// Get is the 404 gate the check endpoint runs BEFORE touching DNS. A domain
// another workspace sends from — even one already checked there — must not
// resolve here.
func TestGetRejectsADomainTheWorkspaceDoesNotSendFrom(t *testing.T) {
	ctx := context.Background()
	q := connect(t)
	store := NewPgStore(q)

	suffix := uuid.NewString()[:8]
	mine, theirs := "mine-"+suffix+".test", "theirs-"+suffix+".test"
	ws := seedWorkspace(t, ctx, q)
	foreign := seedWorkspace(t, ctx, q)
	seedMailbox(t, ctx, q, ws, "a@"+mine)
	seedMailbox(t, ctx, q, foreign, "b@"+theirs)
	if _, err := store.Record(ctx, foreign, dnsauth.Result{
		Domain: theirs,
		SPF:    dnsauth.SPF{Found: true},
		DMARC:  dnsauth.DMARC{Found: true, Policy: "reject"},
	}); err != nil {
		t.Fatalf("seed foreign check: %v", err)
	}

	for _, domain := range []string{theirs, "unrelated-" + suffix + ".test"} {
		if _, err := store.Get(ctx, ws, domain); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) error = %v, want ErrNotFound", domain, err)
		}
	}
	if _, err := store.Get(ctx, ws, mine); err != nil {
		t.Fatalf("Get(own domain): %v", err)
	}
	// The foreign workspace's list never shows this workspace's domain either.
	rows, err := store.List(ctx, foreign)
	if err != nil {
		t.Fatalf("List(foreign): %v", err)
	}
	for _, r := range rows {
		if r.Domain == mine {
			t.Fatalf("workspace %s sees %s", foreign, mine)
		}
	}
}

// The service-level path an operator's Recheck button takes, end to end against
// Postgres: ownership check, lookup through the injected resolver, upsert.
func TestServiceCheckRoundTripsThroughPostgres(t *testing.T) {
	ctx := context.Background()
	q := connect(t)

	ws := seedWorkspace(t, ctx, q)
	domain := "round-" + uuid.NewString()[:8] + ".test"
	seedMailbox(t, ctx, q, ws, "a@"+domain)

	res := &fakeResolver{txt: map[string][]string{
		domain:             {"v=spf1 include:_spf.google.com ~all"},
		"_dmarc." + domain: {"v=DMARC1; p=quarantine"},
	}}
	svc := NewService(NewPgStore(q), res)

	got, err := svc.Check(ctx, ws, domain)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.State != dnsauth.StatePassing || got.DMARCPolicy != "quarantine" || got.CheckedAt == nil {
		t.Fatalf("Check = %+v, want a persisted passing verdict", got)
	}
	stored, err := NewPgStore(q).Get(ctx, ws, domain)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.State != got.State || stored.CheckedAt == nil || !stored.CheckedAt.Equal(*got.CheckedAt) {
		t.Fatalf("stored = %+v, want the response's verdict", stored)
	}

	// A foreign domain 404s without a lookup, even against a real database.
	res.calls = nil
	if _, err := svc.Check(ctx, ws, "victim-"+uuid.NewString()[:8]+".test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Check(foreign) error = %v, want ErrNotFound", err)
	}
	if len(res.calls) != 0 {
		t.Fatalf("lookups performed for a foreign domain: %v", res.calls)
	}
}
