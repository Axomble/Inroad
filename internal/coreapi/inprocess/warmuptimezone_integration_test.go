//go:build integration

package inprocess

import (
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Warmup's waking-hours window must be read in the MAILBOX's timezone, not the
// server's.
//
// Before this, warmup.DueInputs.Loc was never populated anywhere in production
// code, so NextDue fell through to UTC for every participant on the planet: a
// US-based sender warmed up across 03:00-15:00 local, which is precisely the
// unnatural pattern warmup exists to avoid. Campaigns fixed the same bug in
// migration 000031 (campaigns.timezone); this is the warmup half.
//
// Asserted through NextWarmupDue rather than the pure policy, because NextDue
// already honoured Loc — the defect was entirely in the wiring, and a pure test
// passes with or without the fix.
func TestNextWarmupDueUsesTheMailboxTimezone(t *testing.T) {
	ctx, f := setupWarmup(t)

	// Pin the clock to 06:00 UTC. That is inside the waking window in Tokyo
	// (15:00) and outside it in New York (02:00) — one instant, two verdicts,
	// so the zone is the only variable.
	at := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	impl, ok := f.core.(client)
	if !ok {
		t.Fatalf("fixture core is %T, want inprocess.client", f.core)
	}
	impl.now = func() time.Time { return at }
	core := impl

	setZone := func(zone string) {
		t.Helper()
		if _, err := f.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
			MailboxID: f.a, WorkspaceID: f.ws1,
			StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			Timezone: zone,
		}); err != nil {
			t.Fatalf("set timezone %s: %v", zone, err)
		}
	}

	setZone("Asia/Tokyo")
	_, sendNow, err := core.NextWarmupDue(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("NextWarmupDue (Tokyo): %v", err)
	}
	if !sendNow {
		t.Error("06:00 UTC is 15:00 in Tokyo — inside waking hours, so this mailbox should send")
	}

	setZone("America/New_York")
	_, sendNow, err = core.NextWarmupDue(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("NextWarmupDue (New York): %v", err)
	}
	if sendNow {
		t.Error("06:00 UTC is 02:00 in New York — warmup must not send in the middle of the night")
	}

	// An unreadable zone must not take the mailbox out of warmup: fall back to
	// UTC (today's behaviour) rather than erroring the tick.
	setZone("Not/AZone")
	if _, _, err = core.NextWarmupDue(ctx, f.a.String(), f.ws1.String()); err != nil {
		t.Fatalf("an unparseable timezone must fall back to UTC, not fail the tick: %v", err)
	}
}
