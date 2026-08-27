package warmup

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These are pure gen->domain mapping tests: they construct generated rows in
// memory and assert the domain view is a faithful projection. No Postgres, no
// build tag — so the package has executed coverage of its mapping seam even in a
// Docker-less environment (the DB-backed query behavior is covered separately in
// store_integration_test.go under //go:build integration).

func TestParticipantFromGen(t *testing.T) {
	mailboxID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	workspaceID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	started := pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), Valid: true}
	created := pgtype.Timestamptz{Time: time.Date(2026, 7, 2, 9, 30, 0, 0, time.UTC), Valid: true}
	updated := pgtype.Timestamptz{Time: time.Date(2026, 7, 3, 18, 45, 0, 0, time.UTC), Valid: true}
	paused := pgtype.Timestamptz{Time: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC), Valid: true}

	cases := []struct {
		name string
		in   gen.WarmupParticipant
		want Participant
	}{
		{
			name: "zero value maps to zero domain",
			in:   gen.WarmupParticipant{},
			want: Participant{},
		},
		{
			name: "fully populated row projects every field 1:1",
			in: gen.WarmupParticipant{
				MailboxID:     mailboxID,
				WorkspaceID:   workspaceID,
				Enabled:       true,
				StartVolume:   4,
				MaxVolume:     40,
				RampIncrement: 2,
				ReplyRate:     0.3,
				StartedAt:     started,
				HealthState:   "watch",
				HealthReason:  "spam spike",
				PausedUntil:   paused,
				CreatedAt:     created,
				UpdatedAt:     updated,
				// The measurement-reference marker. Orthogonal to both axes: this
				// row is on `watch` AND a sentinel, which is the combination a
				// lane-valued "sentinel" could not express — a sentinel that starts
				// degrading is exactly the case that has to stay representable.
				IsSentinel: true,
			},
			want: Participant{
				MailboxID:     mailboxID,
				WorkspaceID:   workspaceID,
				Enabled:       true,
				StartVolume:   4,
				MaxVolume:     40,
				RampIncrement: 2,
				ReplyRate:     0.3,
				StartedAt:     started,
				HealthState:   "watch",
				HealthReason:  "spam spike",
				PausedUntil:   paused,
				CreatedAt:     created,
				UpdatedAt:     updated,
				IsSentinel:    true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := participantFromGen(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("participantFromGen mismatch\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

func TestDayStatFromGen(t *testing.T) {
	day := pgtype.Date{Time: time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC), Valid: true}

	cases := []struct {
		name string
		in   gen.WarmupDailyStat
		want DayStat
	}{
		{
			name: "zero value maps to zero domain",
			in:   gen.WarmupDailyStat{},
			want: DayStat{},
		},
		{
			name: "counters project without the tenant columns",
			// MailboxID/WorkspaceID exist on the gen row but are intentionally not
			// part of the day-series DTO; the mapping must drop them.
			in: gen.WarmupDailyStat{
				MailboxID:   uuid.MustParse("33333333-3333-3333-3333-333333333333"),
				WorkspaceID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
				Day:         day,
				Sent:        5,
				Received:    4,
				Inbox:       3,
				Spam:        1,
				Replies:     2,
			},
			want: DayStat{
				Day:      day,
				Sent:     5,
				Received: 4,
				Inbox:    3,
				Spam:     1,
				Replies:  2,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dayStatFromGen(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("dayStatFromGen mismatch\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}
