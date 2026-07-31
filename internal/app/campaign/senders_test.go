package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

func TestGetSendersReturnsThePoolAndMode(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	assigned := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{
			{ws, id}: {ID: id, WorkspaceID: ws, RotationMode: rotation.ModeRoundRobin},
		},
		senders: []Sender{
			{MailboxID: uuid.New(), Email: "a@x.test", Weight: 3, Enabled: true, AssignedCount: 7, LastAssignedAt: &assigned},
			{MailboxID: uuid.New(), Email: "b@x.test", Weight: 1, Enabled: false},
		},
	}
	svc := NewService(store, okChecker{active: true})

	pool, err := svc.GetSenders(ctx, ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if pool.RotationMode != rotation.ModeRoundRobin {
		t.Errorf("mode = %q, want %q", pool.RotationMode, rotation.ModeRoundRobin)
	}
	if len(pool.Senders) != 2 {
		t.Fatalf("senders = %d, want 2", len(pool.Senders))
	}
	// Disabled members are returned, not filtered: the panel edits them.
	if pool.Senders[1].Enabled {
		t.Error("the disabled member was reported as enabled")
	}
	if pool.Senders[0].AssignedCount != 7 || pool.Senders[0].LastAssignedAt == nil {
		t.Errorf("rotation state missing from the response: %+v", pool.Senders[0])
	}
}

// The invariant that matters most: a campaign with NO pool rows was never
// configured, not broken. It still sends — from campaigns.mailbox_id — so the read
// must report that implicit one-mailbox pool rather than an empty list that
// contradicts the campaign's behaviour.
func TestGetSendersFallsBackToTheCampaignMailboxWhenThePoolIsEmpty(t *testing.T) {
	ctx := context.Background()
	ws, id, mailbox := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns:      map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, RotationMode: rotation.ModeWeighted}},
		senders:        nil,
		fallbackSender: Sender{MailboxID: mailbox, Email: "solo@x.test", Status: "active", Weight: 1, Enabled: true},
	}
	svc := NewService(store, okChecker{active: true})

	pool, err := svc.GetSenders(ctx, ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(pool.Senders) != 1 {
		t.Fatalf("senders = %d, want the 1-mailbox fallback", len(pool.Senders))
	}
	got := pool.Senders[0]
	if got.MailboxID != mailbox || !got.Enabled || got.Weight != defaultSenderWeight {
		t.Errorf("fallback sender = %+v, want the campaign mailbox enabled at weight 1", got)
	}
	if got.LastAssignedAt != nil || got.AssignedCount != 0 {
		t.Errorf("fallback carries rotation state it cannot have: %+v", got)
	}
}

// A stored mode outside the API's enum can only come from a direct write; the
// read must stay inside the enum rather than fail or emit something the client
// cannot parse.
func TestGetSendersNormalizesAnUnknownRotationMode(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws, RotationMode: "hand_written"}},
		senders:   []Sender{{MailboxID: uuid.New(), Weight: 1, Enabled: true}},
	}
	pool, err := NewService(store, okChecker{active: true}).GetSenders(ctx, ws, id)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if pool.RotationMode != rotation.ModeWeighted {
		t.Errorf("mode = %q, want the %q default", pool.RotationMode, rotation.ModeWeighted)
	}
}

func TestGetSendersCrossTenantIsNotFound(t *testing.T) {
	ctx := context.Background()
	owner, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{owner, id}: {ID: id, WorkspaceID: owner}}}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.GetSenders(ctx, uuid.New(), id); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetSendersReplacesThePoolAndMode(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
	svc := NewService(store, okChecker{active: true})

	in := []SenderInput{
		{MailboxID: uuid.New(), Weight: 5, Enabled: true},
		{MailboxID: uuid.New(), Weight: 100, Enabled: false},
	}
	pool, err := svc.SetSenders(ctx, ws, id, rotation.ModeLRU, in)
	if err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	if store.replacedRotationMode != rotation.ModeLRU || len(store.replacedSenders) != 2 {
		t.Errorf("persisted mode/%d senders = %q/%+v", len(store.replacedSenders), store.replacedRotationMode, store.replacedSenders)
	}
	// The response is re-read, not echoed: it must carry the stored mode.
	if pool.RotationMode != rotation.ModeLRU {
		t.Errorf("returned mode = %q, want %q", pool.RotationMode, rotation.ModeLRU)
	}
}

func TestSetSendersRejectsBadInputWithoutWriting(t *testing.T) {
	dup := uuid.New()
	cases := []struct {
		name    string
		mode    string
		senders []SenderInput
		active  bool
		wantErr error
	}{
		{"empty pool", rotation.ModeWeighted, nil, true, ErrEmptySenderPool},
		{
			name: "unknown mode", mode: "per_send", active: true,
			senders: []SenderInput{{MailboxID: uuid.New(), Weight: 1, Enabled: true}},
			wantErr: ErrRotationMode,
		},
		{
			name: "weight below range", mode: rotation.ModeWeighted, active: true,
			senders: []SenderInput{{MailboxID: uuid.New(), Weight: 0, Enabled: true}},
			wantErr: ErrSenderWeight,
		},
		{
			name: "weight above range", mode: rotation.ModeWeighted, active: true,
			senders: []SenderInput{{MailboxID: uuid.New(), Weight: 101, Enabled: true}},
			wantErr: ErrSenderWeight,
		},
		{
			name: "duplicate mailbox", mode: rotation.ModeWeighted, active: true,
			senders: []SenderInput{{MailboxID: dup, Weight: 1, Enabled: true}, {MailboxID: dup, Weight: 2, Enabled: true}},
			wantErr: ErrDuplicateSender,
		},
		{
			name: "mailbox not in the workspace or not active", mode: rotation.ModeWeighted, active: false,
			senders: []SenderInput{{MailboxID: uuid.New(), Weight: 1, Enabled: true}},
			wantErr: ErrMailboxNotActive,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ws, id := uuid.New(), uuid.New()
			store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
			svc := NewService(store, okChecker{active: tc.active})

			_, err := svc.SetSenders(ctx, ws, id, tc.mode, tc.senders)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if store.replaceSendersCalls != 0 {
				t.Errorf("a rejected pool was written (%d replace calls)", store.replaceSendersCalls)
			}
		})
	}
}

func TestSetSendersCrossTenantIsNotFoundAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	owner, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{owner, id}: {ID: id, WorkspaceID: owner}}}
	svc := NewService(store, okChecker{active: true})

	_, err := svc.SetSenders(ctx, uuid.New(), id, rotation.ModeWeighted,
		[]SenderInput{{MailboxID: uuid.New(), Weight: 1, Enabled: true}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if store.replaceSendersCalls != 0 {
		t.Errorf("a cross-tenant replace reached the store (%d calls)", store.replaceSendersCalls)
	}
}

// A store failure must propagate, not read back as an empty pool.
func TestGetSendersPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	boom := errors.New("pool unavailable")
	store := &fakeStore{
		campaigns:  map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		sendersErr: boom,
	}
	if _, err := NewService(store, okChecker{active: true}).GetSenders(ctx, ws, id); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the store error", err)
	}
}
