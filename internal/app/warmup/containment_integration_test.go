//go:build integration

package warmup

import (
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Containment must follow the ADDRESS, not the mailbox row.
//
// UpsertWarmupParticipant already refuses to release a sealed lane on
// disable -> re-enable. The path these tests pin is the other one: DELETE the
// mailbox and add the same address again. That mints a new mailboxes.id, and a
// carry-forward keyed on the id reads none of the address's history — so
// "remove the mailbox, add it again", two ordinary UI actions, returned a
// quarantined or blocked address to probation, which may send and may take new
// campaign leads.
//
// These run against the QUERY rather than the domain store because the lane is
// the thing under test and warmup.Participant does not carry it: the fix lives
// entirely in SQL, and so does the proof.

// containmentMailbox connects one mailbox at an EXACT address spelling. The
// spelling is the point — the tests re-add the same mailbox under a different
// one — so this deliberately does not canonicalize, unlike the service.
func containmentMailbox(t *testing.T, f fixture, ws uuid.UUID, email string) uuid.UUID {
	t.Helper()
	mb, err := f.q.CreateMailbox(f.ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: "Contained",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %q: %v", email, err)
	}
	return mb.ID
}

// seedSealedTransition records one transition into a sealed lane, mirroring the
// columns ApplyWarmupParticipantTransition writes. It derives every mailbox fact
// from the mailboxes row exactly as that writer does, so the fixture cannot
// drift from the writer it stands in for.
func seedSealedTransition(t *testing.T, f fixture, ws, mailbox uuid.UUID, lane string) {
	t.Helper()
	tag, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_state_transitions (
		     workspace_id, mailbox_id, mailbox_email, from_state, to_state, reason_code, reason,
		     from_lane, to_lane, lane_reason_code, lane_reason,
		     placement_samples, spam_rate, bounce_samples, bounce_rate,
		     complaint_samples, complaint_rate, invalid_tokens, policy_version)
		 SELECT m.workspace_id, m.id, lower(btrim(m.email)), 'healthy', 'paused',
		        'spam_pause', 'spam placement rate above the pause threshold',
		        'healthy', $3, 'lane_quarantined', 'contained',
		        40, 0.4, 200, 0.01, 1000, 0.0002, 0, 'warmup-phase1-v1'
		   FROM mailboxes m WHERE m.id = $2 AND m.workspace_id = $1`,
		ws, mailbox, lane)
	if err != nil {
		t.Fatalf("seed %s transition: %v", lane, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("seed %s transition wrote %d rows, want 1", lane, tag.RowsAffected())
	}
}

// enableWarmup runs the upsert under test and returns the lane it settled on.
func enableWarmup(t *testing.T, f fixture, ws, mailbox uuid.UUID) string {
	t.Helper()
	p, err := f.q.UpsertWarmupParticipant(f.ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mailbox, WorkspaceID: ws,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	})
	if err != nil {
		t.Fatalf("enable warmup: %v", err)
	}
	return p.Lane
}

// countTransitions counts what the address's trail still holds, whatever mailbox
// row each entry was written against — which is the point, so it counts by
// address and not by id.
func countTransitions(t *testing.T, f fixture, ws uuid.UUID, email string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM warmup_state_transitions t
		 WHERE t.workspace_id = $1 AND t.mailbox_email = lower(btrim($2::text))`,
		ws, email).Scan(&n); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	return n
}

// The laundering path, end to end: quarantine an address, delete the mailbox,
// add the address again. Every spelling of the same address must come back
// contained — a case-different re-spelling is what a second operator typing the
// address by hand actually produces, and it is the whole exploit if the identity
// is compared literally.
func TestReAddingADeletedMailboxKeepsItsContainment(t *testing.T) {
	f := setup(t)

	for _, tc := range []struct {
		name      string
		respelled string
	}{
		{"same spelling", "sealed@x.test"},
		{"different case", "Sealed@X.TEST"},
		{"surrounding whitespace", "  sealed@x.test  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := f.q.CreateWorkspace(f.ctx, "Containment "+uuid.NewString())
			if err != nil {
				t.Fatalf("workspace: %v", err)
			}
			const address = "sealed@x.test"
			first := containmentMailbox(t, f, ws.ID, address)
			if lane := enableWarmup(t, f, ws.ID, first); lane != "probation" {
				t.Fatalf("a brand-new address enabled into %q, want probation", lane)
			}
			seedSealedTransition(t, f, ws.ID, first, "quarantine")

			rows, err := f.q.DeleteMailbox(f.ctx, gen.DeleteMailboxParams{ID: first, WorkspaceID: ws.ID})
			if err != nil {
				t.Fatalf("delete mailbox: %v", err)
			}
			if rows != 1 {
				t.Fatalf("delete mailbox affected %d rows, want 1", rows)
			}

			// The trail is what containment rests on. If deleting the mailbox takes
			// it with them, no lookup can restore the lane, whatever it is keyed on.
			if n := countTransitions(t, f, ws.ID, address); n != 1 {
				t.Fatalf("after deleting the mailbox the address kept %d transitions, want 1: "+
					"the containment record must outlive the row it was written against", n)
			}

			second := containmentMailbox(t, f, ws.ID, tc.respelled)
			if second == first {
				t.Fatal("re-adding the address reused the mailbox id, so this proves nothing")
			}
			if lane := enableWarmup(t, f, ws.ID, second); lane != "quarantine" {
				t.Fatalf("lane after delete + re-add as %q = %q, want quarantine: "+
					"deleting a mailbox and adding the address again must not launder its containment",
					tc.respelled, lane)
			}
		})
	}
}

// Containment is pinned to the workspace that earned it. The same address in
// another tenant is a different mailbox belonging to different people, and must
// enter at probation like any other new address.
func TestContainmentDoesNotCrossWorkspaces(t *testing.T) {
	f := setup(t)
	const address = "shared@x.test"

	sealedWS, err := f.q.CreateWorkspace(f.ctx, "Sealed "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	contained := containmentMailbox(t, f, sealedWS.ID, address)
	seedSealedTransition(t, f, sealedWS.ID, contained, "blocked")

	otherWS, err := f.q.CreateWorkspace(f.ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	stranger := containmentMailbox(t, f, otherWS.ID, address)
	if lane := enableWarmup(t, f, otherWS.ID, stranger); lane != "probation" {
		t.Fatalf("another tenant's %q enabled into %q, want probation: one workspace's containment "+
			"must not read as another's", address, lane)
	}

	// ...and the workspace that earned it still holds it.
	if lane := enableWarmup(t, f, sealedWS.ID, contained); lane != "blocked" {
		t.Fatalf("the contained workspace's own lane = %q, want blocked", lane)
	}
}

// A different address in the SAME workspace is not contained by its neighbour.
// Guards the other direction of the re-key: a lookup that lost its address
// predicate would seal every mailbox a workspace ever adds.
func TestContainmentDoesNotSpreadToOtherAddresses(t *testing.T) {
	f := setup(t)

	ws, err := f.q.CreateWorkspace(f.ctx, "Neighbours "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	contained := containmentMailbox(t, f, ws.ID, "sealed@x.test")
	seedSealedTransition(t, f, ws.ID, contained, "quarantine")

	neighbour := containmentMailbox(t, f, ws.ID, "clean@x.test")
	if lane := enableWarmup(t, f, ws.ID, neighbour); lane != "probation" {
		t.Fatalf("an unrelated address in the same workspace enabled into %q, want probation", lane)
	}
}

// The path that already worked, re-asserted here so the re-key cannot silently
// regress it: DISABLE deletes the participant row without touching the mailbox,
// and re-enabling must still find the sealed lane. Only quarantine and blocked
// carry forward — a mailbox that legitimately left them has a later transition
// saying so, and that later row must win.
func TestDisableThenReEnableStillKeepsContainment(t *testing.T) {
	f := setup(t)

	for _, tc := range []struct {
		name string
		lane string
		want string
	}{
		{"quarantine carries forward", "quarantine", "quarantine"},
		{"blocked carries forward", "blocked", "blocked"},
		{"an unsealed lane does not", "watch", "probation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := f.q.CreateWorkspace(f.ctx, "Re-enable "+uuid.NewString())
			if err != nil {
				t.Fatalf("workspace: %v", err)
			}
			mb := containmentMailbox(t, f, ws.ID, "reenable@x.test")
			if lane := enableWarmup(t, f, ws.ID, mb); lane != "probation" {
				t.Fatalf("first enable landed in %q, want probation", lane)
			}
			seedSealedTransition(t, f, ws.ID, mb, tc.lane)

			if _, err := f.q.DisableWarmupParticipant(f.ctx, gen.DisableWarmupParticipantParams{
				MailboxID: mb, WorkspaceID: ws.ID,
			}); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if lane := enableWarmup(t, f, ws.ID, mb); lane != tc.want {
				t.Fatalf("lane after disable + re-enable = %q, want %q", lane, tc.want)
			}
		})
	}
}
