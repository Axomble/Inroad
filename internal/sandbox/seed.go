package sandbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// DefaultContacts is the population a run seeds when the caller does not ask
// for a size. Sized so the funnel produces enough of everything to be worth
// looking at: past the 100-row max page size (so paging is exercisable), and
// large enough that the ~8% reply rate yields a couple of dozen inbox threads
// spread across every reply label rather than the two a 150-contact run gave.
const DefaultContacts = 300

// SeedInput describes the workspace to populate. The workspace itself must
// already exist: it is created by cmd/seed through identity.Service.Register,
// so it gets its DEK, its reply labels and its owner exactly as a real signup
// does. Fabricating one here would produce a workspace subtly unlike every
// other workspace in the system.
type SeedInput struct {
	WorkspaceID uuid.UUID
	// Contacts is the population size; <= 0 means DefaultContacts.
	Contacts int
	// MailboxEmail/MailboxName identify the sending mailbox the harness
	// creates and runs the campaign from.
	MailboxEmail string
	MailboxName  string
	// Options tune the simulated run itself.
	Options Options
}

// Sending-mailbox defaults. The domain is a .test one on purpose: it can
// never resolve, so nothing the harness creates could deliver to a real
// recipient even if it were somehow pointed at a live sender.
const (
	DefaultMailboxEmail = "rowan@outbound.inroad.test"
	DefaultMailboxName  = "Rowan Ellis"
)

// Seeder creates the campaign scaffolding a run needs (mailbox, list,
// contacts, sequence steps) and then hands off to the Simulator for the
// history. It writes the scaffolding through the generated queries — those
// rows carry no meaningful timestamp, so there is nothing to backdate and no
// reason to hand-write SQL for them.
type Seeder struct {
	q      *gen.Queries
	store  Store
	enroll EnrollmentWriter
}

// NewSeeder builds a Seeder over the generated queries and the simulation
// store.
func NewSeeder(q *gen.Queries, store Store, enroll EnrollmentWriter) *Seeder {
	return &Seeder{q: q, store: store, enroll: enroll}
}

// Seed populates the workspace and returns what it wrote.
//
// The guard is NOT checked here: it is checked once at the command boundary,
// before a pool is even opened, so a refused run never connects to the
// database at all. Enforcing it twice would invite the two checks to drift.
func (s *Seeder) Seed(ctx context.Context, in SeedInput) (Result, error) {
	if in.WorkspaceID == uuid.Nil {
		return Result{}, fmt.Errorf("sandbox: workspace id is required")
	}
	if in.Contacts <= 0 {
		in.Contacts = DefaultContacts
	}
	if in.MailboxEmail == "" {
		in.MailboxEmail, in.MailboxName = DefaultMailboxEmail, DefaultMailboxName
	}

	target, err := s.scaffold(ctx, in)
	if err != nil {
		return Result{}, err
	}
	res, err := NewSimulator(s.store, s.enroll, in.Options).Run(ctx, target)
	if err != nil {
		return res, fmt.Errorf("simulate: %w", err)
	}
	return res, nil
}

// scaffold creates the rows the simulated history hangs off: the sending
// mailbox, the target list, the contacts, and the running campaign with its
// sequence steps.
func (s *Seeder) scaffold(ctx context.Context, in SeedInput) (Target, error) {
	mailbox, err := s.createMailbox(ctx, in)
	if err != nil {
		return Target{}, fmt.Errorf("mailbox: %w", err)
	}

	list, err := s.q.CreateList(ctx, gen.CreateListParams{
		WorkspaceID: in.WorkspaceID, Name: "Sandbox — simulated prospects",
	})
	if err != nil {
		return Target{}, fmt.Errorf("list: %w", err)
	}

	contacts, err := s.createContacts(ctx, in, list.ID)
	if err != nil {
		return Target{}, fmt.Errorf("contacts: %w", err)
	}

	campaign, err := s.createCampaign(ctx, in, mailbox.ID, list.ID)
	if err != nil {
		return Target{}, fmt.Errorf("campaign: %w", err)
	}

	return Target{
		WorkspaceID: in.WorkspaceID, CampaignID: campaign, MailboxID: mailbox.ID,
		MailboxEmail: in.MailboxEmail, MailboxName: in.MailboxName, Contacts: contacts,
	}, nil
}

// SMTP defaults for the seeded mailbox. The ports satisfy the provider-aware
// CHECK constraint (migration 000057) that requires a real port on an smtp
// mailbox; the host is unreachable by design.
const (
	sandboxSMTPPort = 587
	sandboxIMAPPort = 993
	// sandboxDailyCap is high enough that the simulated volume never looks
	// throttled on the mailbox screen.
	sandboxDailyCap = 500
)

// sandboxCiphertext stands in for the sealed mailbox secret. It is
// deliberately NOT decryptable: the harness never sends through this mailbox
// (delivery, when enabled, goes straight to the local catcher), so a real
// sealed credential would be a secret stored for no reason.
const sandboxCiphertext = "sandbox-not-a-real-ciphertext"

func (s *Seeder) createMailbox(ctx context.Context, in SeedInput) (gen.Mailbox, error) {
	return s.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: in.WorkspaceID, Provider: "smtp",
		Email: in.MailboxEmail, DisplayName: in.MailboxName,
		SecretCiphertext: sandboxCiphertext,
		SmtpHost:         "smtp.outbound.inroad.test", SmtpPort: sandboxSMTPPort, SmtpUsername: in.MailboxEmail,
		ImapHost: "imap.outbound.inroad.test", ImapPort: sandboxIMAPPort, ImapUsername: in.MailboxEmail,
		DailyCap: sandboxDailyCap, MinIntervalSeconds: 60,
		// Ramp off: the simulated history backdates its own volume, and a ramp
		// would make the mailbox screen claim a cap the seeded sends exceeded.
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
}

// createContacts writes one contact per persona and adds it to the list.
func (s *Seeder) createContacts(ctx context.Context, in SeedInput, listID uuid.UUID) ([]TargetContact, error) {
	personas := BuildPersonas(in.Contacts)
	out := make([]TargetContact, 0, len(personas))
	for _, p := range personas {
		c, err := s.q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: in.WorkspaceID, Email: p.Email,
			FirstName: p.FirstName, LastName: p.LastName, Company: p.Company,
		})
		if err != nil {
			return nil, fmt.Errorf("upsert %s: %w", p.Email, err)
		}
		if err := s.q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: c.ID}); err != nil {
			return nil, fmt.Errorf("list member %s: %w", p.Email, err)
		}
		out = append(out, TargetContact{ContactID: c.ID, Persona: p})
	}
	return out, nil
}

// createCampaign creates the campaign and its steps, then marks it running.
//
// Running, not draft: the harness seeds a campaign that has ALREADY sent, and
// a draft carrying thousands of completed sends would be a state the product
// can never produce. (cmd/seed's own fixture campaign stays draft for the
// opposite and equally correct reason — it has no history and launching it
// would enqueue real work.)
func (s *Seeder) createCampaign(ctx context.Context, in SeedInput, mailboxID, listID uuid.UUID) (uuid.UUID, error) {
	first := campaignSteps[0]
	c, err := s.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: in.WorkspaceID, Name: "Sandbox — Q3 outbound simulation",
		MailboxID: mailboxID, ListID: listID,
		Subject: first.Subject, BodyText: first.BodyText, BodyHtml: first.BodyHTML(),
	})
	if err != nil {
		return uuid.Nil, err
	}
	for _, step := range campaignSteps {
		if _, err := s.q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: in.WorkspaceID, CampaignID: c.ID,
			StepOrder: step.Order, DelaySeconds: step.DelaySeconds,
			Subject: step.Subject, BodyText: step.BodyText, BodyHtml: step.BodyHTML(),
		}); err != nil {
			return uuid.Nil, fmt.Errorf("step %d: %w", step.Order, err)
		}
	}

	// launched_at is backdated to the start of the simulated window, so the
	// campaign's own age agrees with the history hanging off it: a campaign
	// launched "just now" carrying three weeks of sends reads as corrupt.
	if err := s.q.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID: c.ID, WorkspaceID: in.WorkspaceID, Status: "running",
		LaunchedAt: pgtype.Timestamptz{Time: s.launchedAt(in), Valid: true},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("set running: %w", err)
	}
	return c.ID, nil
}

// launchedAt is the instant the simulated campaign started sending: the far
// edge of the run's history window.
func (s *Seeder) launchedAt(in SeedInput) time.Time {
	now, window := in.Options.Now, in.Options.Window
	if now.IsZero() {
		now = time.Now()
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return now.Add(-window)
}
