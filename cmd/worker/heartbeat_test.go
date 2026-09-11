package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/inroad/inroad/internal/worker"
)

// fakeHeartbeatClient counts calls so a test can assert whether startHeartbeat
// ever reached the registry, without a fake satisfying coreapi.Client's dozens
// of other methods.
type fakeHeartbeatClient struct {
	calls int
}

func (f *fakeHeartbeatClient) UpsertWorkerHeartbeat(_ context.Context, _, _ string) error {
	f.calls++
	return nil
}

// A control-role host has no per-message handlers registered (worker.Register),
// so if it heartbeated it would become assignable — and an assignment is what
// routes a mailbox's warmup:tick to "w:<worker_id>" (the only task type any
// assignment redirects; everything else rides the shared `send` queue). Per
// worker.QueuesFor, a control role does not even consume "w:<worker_id>", so an
// assignment to one would leave that queue undrained rather than dead-lettering
// — which is exactly why it must never become assignable in the first place.
func TestStartHeartbeatControlRoleNeverRegisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	core := &fakeHeartbeatClient{}
	startHeartbeat(ctx, core, "worker-1", "", worker.RoleControl, slog.Default())

	if core.calls != 0 {
		t.Errorf("control role called UpsertWorkerHeartbeat %d times, want 0: a control host must never become assignable", core.calls)
	}
}

// RoleAll is the self-host topology: the single worker must stay assignable,
// since it is the only place any mailbox's per-message work could run.
func TestStartHeartbeatAllRoleRegisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	core := &fakeHeartbeatClient{}
	startHeartbeat(ctx, core, "worker-1", "", worker.RoleAll, slog.Default())

	if core.calls != 1 {
		t.Errorf("all role called UpsertWorkerHeartbeat %d times on start, want 1", core.calls)
	}
}

// A send-role host is exactly what the assigner must be able to route mailboxes
// to, so it must heartbeat.
func TestStartHeartbeatSendRoleRegisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	core := &fakeHeartbeatClient{}
	startHeartbeat(ctx, core, "worker-1", "", worker.RoleSend, slog.Default())

	if core.calls != 1 {
		t.Errorf("send role called UpsertWorkerHeartbeat %d times on start, want 1", core.calls)
	}
}
