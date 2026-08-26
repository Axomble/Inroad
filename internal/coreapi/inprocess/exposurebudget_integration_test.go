//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These exercise the exposure budget against Postgres: the fault domain resolved
// from real mailbox rows, the shares read from real campaign_senders counters, and
// the workspace pinning that keeps one tenant's lanes out of another's ceilings.
// Docker must be up.

// setEmail moves a fixture mailbox to another address. The address is the point:
// the budget groups by the organizational domain of mailboxes.email, so a
// two-domain pool has to be built by choosing the hosts.
func (f poolFixture) setEmail(t *testing.T, ctx context.Context, mailboxID uuid.UUID, email string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE mailboxes SET email = $1, smtp_username = $1, imap_username = $1
		  WHERE id = $2 AND workspace_id = $3`, email, mailboxID, f.ws); err != nil {
		t.Fatalf("set mailbox email %q: %v", email, err)
	}
}

// setAssigned writes a pool member's rotation history — the "current usage" the
// share is computed from. Set directly rather than by driving sends, because the
// counter is what the budget reads and a hundred real assignments would prove the
// same thing far more slowly.
func (f poolFixture) setAssigned(t *testing.T, ctx context.Context, mailboxID uuid.UUID, n int64) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE campaign_senders SET assigned_count = $1
		  WHERE campaign_id = $2 AND mailbox_id = $3 AND workspace_id = $4`,
		n, f.campaignID, mailboxID, f.ws); err != nil {
		t.Fatalf("set assigned_count: %v", err)
	}
}

// laneInWorkspace makes a mailbox an enabled warmup participant in another
// workspace, which is how the pinning test builds a foreign verdict on a name this
// workspace also sends from.
func (f poolFixture) laneInWorkspace(t *testing.T, ctx context.Context, ws uuid.UUID, email, lane string) {
	t.Helper()
	mb, err := f.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: email,
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("foreign mailbox %s: %v", email, err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, lane) VALUES ($1,$2,$3)`,
		mb.ID, ws, lane); err != nil {
		t.Fatalf("foreign participant lane %s: %v", lane, err)
	}
}

// twoDomainPool puts A on heavy.test carrying 90 of the campaign's 100 assignments
// and B on light.test carrying 10, with A weighted and capped so that it wins every
// rotation mode outright. A result of B can therefore only be the budget's doing.
func twoDomainPool(t *testing.T, ctx context.Context, f poolFixture) {
	t.Helper()
	f.setEmail(t, ctx, f.mailboxA, "bulk-"+uuid.NewString()+"@heavy.test")
	f.setEmail(t, ctx, f.mailboxB, "solo-"+uuid.NewString()+"@light.test")
	f.addSender(t, ctx, f.mailboxA, 100, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.setAssigned(t, ctx, f.mailboxA, 90)
	f.setAssigned(t, ctx, f.mailboxB, 10)
}

// The payoff: 90% of a campaign resting on one organizational domain routes the next
// contact to the under-exposed one, even though the dominant mailbox outranks it on
// every rule the pool itself has.
func TestLopsidedTwoDomainPoolShiftsTowardTheUnderExposedDomain(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	twoDomainPool(t, ctx, f)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the under-exposed B %s — heavy.test already carries 90%% of the "+
			"campaign, so a blocklisting there costs 90%% of its sending", job.MailboxID, f.mailboxB)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != f.mailboxB {
		t.Errorf("pinned = %s, want B %s", got, f.mailboxB)
	}
}

// The control for the test above, and the property this slice is most likely to
// break: the same lopsided history on ONE domain must select exactly what it did
// before exposure budgets existed. The whole campaign rests on acme.test either way,
// and narrowing to nothing would stop mail today to avoid losing mail if something
// failed later.
func TestSingleDomainPoolSelectsExactlyAsItDidBefore(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	// Both fixture mailboxes are already on acme.test; B is moved to a SUBDOMAIN, so
	// this also covers the grouping being organizational rather than per host.
	f.setEmail(t, ctx, f.mailboxB, "solo-"+uuid.NewString()+"@mail.acme.test")
	f.addSender(t, ctx, f.mailboxA, 100, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.setAssigned(t, ctx, f.mailboxA, 95)
	f.setAssigned(t, ctx, f.mailboxB, 5)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want the pool's own winner A %s — with one fault domain there is no "+
			"alternative, and the budget must be inert rather than fatal", job.MailboxID, f.mailboxA)
	}
}

// Two mailboxes whose addresses cannot be grouped are two unknowns, not one shared
// fault domain. Bucketing them under a single empty key would throttle 90% of this
// pool for a fate we never established it shares.
//
// The THIRD mailbox, on a real domain, is what gives this test teeth. Without it the
// two unknowns would be 100% of the pool once bucketed, every candidate would be over
// budget, and the never-empty rule would hand back the full set — so a bucketing bug
// would look identical to correct behaviour. With it, bucketing drops the two and
// hands the contact to known.test.
func TestUnclassifiableMailboxesAreNotBucketedTogether(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	// No '@', so warmup.SharedReputationDomain yields "" for both. Undeliverable in
	// practice, and precisely the row the fold must refuse to group.
	f.setEmail(t, ctx, f.mailboxA, "no-domain-a-"+uuid.NewString())
	f.setEmail(t, ctx, f.mailboxB, "no-domain-b-"+uuid.NewString())
	known := f.addMailbox(t, ctx, "known-"+uuid.NewString()+"@known.test")
	f.addSender(t, ctx, f.mailboxA, 100, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.addSender(t, ctx, known, 1, true)
	f.setAssigned(t, ctx, f.mailboxA, 45)
	f.setAssigned(t, ctx, f.mailboxB, 45)
	f.setAssigned(t, ctx, known, 10)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want the pool's own winner A %s — an unclassifiable mailbox is not "+
			"known to share a failure with anything, so it is never over budget", job.MailboxID, f.mailboxA)
	}
}

// gradedThreeDomainPool spreads the campaign over THREE domains at shares that sit
// between the ceilings: heavy.test 25%, light.test 40%, third.test 35%. Every one is under the
// flat 0.6 cap, so nothing moves unless a LANE ceiling moves it — and 25% is inside
// watch's 0.35 but outside recovery's 0.20, which is what lets one fixture tell a
// tightened ceiling from an untightened one.
//
// heavy.test's mailbox wins ModeWeighted outright (100x the weight) and light.test's
// is the runner-up at 10x, so both "A still wins" and "A is gone, B took it" are
// unambiguous rather than a tie broken by a random uuid.
//
// Returns the third mailbox; A and B are the fixture's own.
func gradedThreeDomainPool(t *testing.T, ctx context.Context, f poolFixture) uuid.UUID {
	t.Helper()
	c := f.addMailbox(t, ctx, "third-"+uuid.NewString()+"@third.test")
	f.setEmail(t, ctx, f.mailboxA, "bulk-"+uuid.NewString()+"@heavy.test")
	f.setEmail(t, ctx, f.mailboxB, "solo-"+uuid.NewString()+"@light.test")
	f.addSender(t, ctx, f.mailboxA, 100, true)
	f.addSender(t, ctx, f.mailboxB, 10, true)
	f.addSender(t, ctx, c, 1, true)
	f.setAssigned(t, ctx, f.mailboxA, 25)
	f.setAssigned(t, ctx, f.mailboxB, 40)
	f.setAssigned(t, ctx, c, 35)
	return c
}

// Another tenant's lane must never tighten this one's ceiling. Both workspaces send
// from heavy.test; the FOREIGN one has a mailbox in recovery there, whose 0.20
// ceiling would remove this campaign's dominant mailbox if the lane fold leaked
// across tenants.
func TestExposureCeilingsArePinnedToTheWorkspace(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	gradedThreeDomainPool(t, ctx, f)
	f.laneInWorkspace(t, ctx, f.foreignWS, "intruder-"+uuid.NewString()+"@heavy.test", warmup.LaneRecovery)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want A %s — heavy.test is at 25%% and not warming up IN THIS WORKSPACE; "+
			"only a foreign tenant's recovery lane could have narrowed it away", job.MailboxID, f.mailboxA)
	}
}

// The reactive half end to end, and the pair that gives the pinning test above its
// meaning: this workspace's OWN recovery lane on heavy.test does tighten the ceiling
// at the identical 25% share. Without it, the pinning test could pass because the
// ceiling never works at all.
func TestOwnRecoveryLaneTightensTheCeilingAtTheSameShare(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	gradedThreeDomainPool(t, ctx, f)
	f.setLane(t, ctx, f.mailboxA, warmup.LaneRecovery)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the runner-up B %s — a domain re-earning trust should carry less of "+
			"the campaign than a healthy one, not the same share until it is cut off entirely",
			job.MailboxID, f.mailboxB)
	}
}

// The ordering the ceilings themselves encode, at one fixed share: WATCH tolerates
// 25% and RECOVERY does not. Load-bearing next to the test above — without it,
// "recovery narrowed the set" could just as well mean "any lane at all narrows it",
// which would make the graduation a fiction.
func TestWatchToleratesAShareRecoveryRefuses(t *testing.T) {
	for _, tc := range []struct {
		lane string
		want string // "A" keeps the dominant mailbox, "B" means it was narrowed away
	}{
		{warmup.LaneWatch, "A"},
		{warmup.LaneRecovery, "B"},
	} {
		t.Run(tc.lane, func(t *testing.T) {
			ctx, f := setupPool(t)
			f.setRotationMode(t, ctx, rotation.ModeWeighted)
			gradedThreeDomainPool(t, ctx, f)
			f.setLane(t, ctx, f.mailboxA, tc.lane)

			enrollmentID := f.enroll(t, ctx)
			job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
			if err != nil {
				t.Fatalf("GetStepSendJob: %v", err)
			}
			want := f.mailboxA
			if tc.want == "B" {
				want = f.mailboxB
			}
			if job.MailboxID != want.String() {
				t.Errorf("lane %s: mailbox = %s, want %s — watch's ceiling is %v and recovery's is %v, "+
					"against a 25%% share", tc.lane, job.MailboxID, want,
					warmup.WatchExposureCeiling, warmup.RecoveryExposureCeiling)
			}
		})
	}
}
