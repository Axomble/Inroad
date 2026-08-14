package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The seeded workspace exists to be clicked through, so it has to be big enough
// that the screens behave the way they will in production. A workspace with three
// contacts hides everything interesting: pagination can't page, search has nothing
// to discriminate, and a sender pool with one mailbox never rotates.
const (
	seedContacts    = 150 // > the 100 max page size, so paging is exercisable
	seedDailyCap    = 60
	seedRampDays    = 30
	seedRampStartCa = 5

	// The columns are CHECK-constrained to 1..65535 (migration 000028), so even a
	// provider-API mailbox that never reads them needs a valid value.
	defaultSMTPPort = 587
	defaultIMAPPort = 993
)

// seedCiphertext stands in for an envelope-encrypted mailbox secret. It is not
// decryptable, which is correct: these mailboxes point at example.com and cannot
// send. The seed exists to make the UI explorable, not to deliver mail — connect a
// real mailbox (or a local GreenMail, which speaks both SMTP and IMAP) for that.
const seedCiphertext = "seed-not-a-real-ciphertext"

// domains and companies give search something to discriminate on: a query has to
// be able to match a domain, a surname, or a company and return different rows.
var (
	domains   = []string{"northwind", "lumen", "fernhill", "sablepoint", "arcadia"}
	companies = []string{"Northwind", "Lumen", "Fernhill", "Sablepoint", "Arcadia"}
	firsts    = []string{"Ana", "Liam", "Yuki", "Noor", "Theo", "Mira", "Kai", "Sofia"}
	lasts     = []string{"Silva", "Novak", "Tanaka", "Haddad", "Berg", "Okafor", "Lindqvist", "Rossi"}
)

// seedFixtures fills a freshly registered workspace with mailboxes, lists,
// contacts, and a running campaign. Every insert is additive and the caller runs
// it once, right after Register, so there is nothing to make idempotent.
func seedFixtures(ctx context.Context, q *gen.Queries, ws uuid.UUID) (string, error) {
	mailboxes, err := seedMailboxes(ctx, q, ws)
	if err != nil {
		return "", fmt.Errorf("mailboxes: %w", err)
	}
	list, err := seedContactsAndList(ctx, q, ws)
	if err != nil {
		return "", fmt.Errorf("contacts: %w", err)
	}
	if err := seedCampaign(ctx, q, ws, mailboxes[0], list); err != nil {
		return "", fmt.Errorf("campaign: %w", err)
	}
	return fmt.Sprintf("%d mailboxes, %d contacts, 1 list, 1 campaign",
		len(mailboxes), seedContacts), nil
}

// seedMailboxes creates one mailbox per provider so the mailbox screen shows all
// three connection types, and pauses the last one so the health/gating states in
// the senders panel have something to render.
func seedMailboxes(ctx context.Context, q *gen.Queries, ws uuid.UUID) ([]uuid.UUID, error) {
	specs := []struct {
		provider, email, name string
		cap                   int32
	}{
		{"smtp", "alex@outbound.example.com", "Alex (SMTP)", seedDailyCap},
		{"gmail", "jordan@outbound.example.com", "Jordan (Gmail)", 45},
		{"m365", "sam@outbound.example.com", "Sam (Microsoft 365)", 25},
	}

	ids := make([]uuid.UUID, 0, len(specs))
	for _, s := range specs {
		// Ports are SMTP-only. 000028 constrained both to 1..65535 for every
		// provider, and 000057 replaced that with a provider-aware CHECK requiring
		// gmail/m365 to carry 0 — the transport for those is the provider API, so a
		// port is not merely unread but wrong. Seeding the old defaults for all
		// three made `go run ./cmd/seed` fail the CHECK on a fresh database, which
		// broke the documented `make dev` bootstrap.
		params := gen.CreateMailboxParams{
			WorkspaceID: ws, Provider: s.provider, Email: s.email, DisplayName: s.name,
			SecretCiphertext: seedCiphertext,
			DailyCap:         s.cap, MinIntervalSeconds: 120,
			RampEnabled: true, RampStartCap: seedRampStartCa, RampDays: seedRampDays,
		}
		// Hosts and usernames are SMTP-only for the same reason: inventing them for
		// an API transport would misrepresent how that connection actually works.
		if s.provider == "smtp" {
			params.SmtpPort, params.ImapPort = defaultSMTPPort, defaultIMAPPort
			params.SmtpHost, params.SmtpUsername = "smtp.example.com", s.email
			params.ImapHost, params.ImapUsername = "imap.example.com", s.email
		}
		m, err := q.CreateMailbox(ctx, params)
		if err != nil {
			return nil, err
		}
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// seedContactsAndList creates the target list and enough contacts that the
// contacts screen behaves like a real one: more than a page, with varied names,
// domains and companies so search returns different rows for different queries.
func seedContactsAndList(ctx context.Context, q *gen.Queries, ws uuid.UUID) (uuid.UUID, error) {
	list, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws, Name: "Q3 — Seed-stage SaaS founders"})
	if err != nil {
		return uuid.Nil, err
	}
	// A second, empty list so the list switcher has something to switch to and the
	// empty-state copy is reachable without deleting anything.
	if _, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws, Name: "Conference follow-up — SaaStr"}); err != nil {
		return uuid.Nil, err
	}

	for i := range seedContacts {
		c, err := q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: ws,
			Email:       fmt.Sprintf("%s.%s%d@%s.example.com", firsts[i%len(firsts)], lasts[i%len(lasts)], i, domains[i%len(domains)]),
			FirstName:   firsts[i%len(firsts)],
			LastName:    lasts[i%len(lasts)],
			Company:     companies[i%len(companies)],
		})
		if err != nil {
			return uuid.Nil, err
		}
		if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: list.ID, ContactID: c.ID}); err != nil {
			return uuid.Nil, err
		}
	}
	return list.ID, nil
}

// seedCampaign creates a three-step sequence in draft. Draft rather than running
// on purpose: launching would enqueue real sends against mailboxes that cannot
// deliver, filling the logs with failures the first time someone starts the worker.
// Launching it is the first thing a reader can do themselves.
func seedCampaign(ctx context.Context, q *gen.Queries, ws, mailbox, list uuid.UUID) error {
	steps := []struct {
		order   int32
		delay   int32
		subject string
		body    string
	}{
		{1, 0, "A quick idea for {{company}}", "Hi {{first_name}},\n\nSaw what {{company}} is building..."},
		{2, 3 * 24 * 3600, "", "Bumping this in case it got buried."},
		{3, 5 * 24 * 3600, "", "Last note from me — happy to close the loop."},
	}

	c, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws, Name: "Q3 outbound — seed-stage SaaS", MailboxID: mailbox, ListID: list,
		Subject: steps[0].subject, BodyText: steps[0].body,
	})
	if err != nil {
		return err
	}
	for _, s := range steps {
		if _, err := q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: ws, CampaignID: c.ID, StepOrder: s.order, DelaySeconds: s.delay,
			Subject: s.subject, BodyText: s.body,
		}); err != nil {
			return err
		}
	}
	return nil
}
