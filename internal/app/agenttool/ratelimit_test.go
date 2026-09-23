package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// countingLimiter is a real fixed-window budget, not a canned answer: it allows
// `limit` calls per key and refuses the rest, so a test can drive a tool past
// its budget and watch the refusal land on exactly the next call.
type countingLimiter struct {
	mu    sync.Mutex
	used  map[string]int
	keys  []string
	limit int
}

func newCountingLimiter() *countingLimiter { return &countingLimiter{used: map[string]int{}} }

func (c *countingLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if window != time.Minute {
		return false, fmt.Errorf("unexpected window %s", window)
	}
	c.limit = limit
	c.keys = append(c.keys, key)
	c.used[key]++
	return c.used[key] <= limit, nil
}

func contactWriteCalls() map[string]string {
	return map[string]string{
		methodCreate:        `{"loading_message":"Adding","method":"create","email":"dana@acme.test"}`,
		methodAddToList:     fmt.Sprintf(`{"loading_message":"Adding","method":"add_to_list","contact_id":%q,"list_id":%q}`, contactA, listA),
		methodLinkCompany:   linkArgs(contactA.String(), companyA.String()),
		methodUnlinkCompany: fmt.Sprintf(`{"loading_message":"Unlinking","method":"unlink_company","contact_id":%q}`, contactA),
	}
}

// Every method of inroad_contact_write writes, so every method is metered —
// including the company link, which is what the security review flagged.
func TestContactWriteEveryMethodStopsAtTheLimitWithoutWriting(t *testing.T) {
	for method, args := range contactWriteCalls() {
		t.Run(method, func(t *testing.T) {
			limiter := newCountingLimiter()
			writes := &fakeContactWrites{id: contactA, created: true}
			reg := New(Deps{ContactWrites: writes, WriteLimiter: limiter})

			for i := 0; i < agentWritesPerMinute; i++ {
				ok(t, reg, member(), "inroad_contact_write", args)
			}
			// Reset the recorded call so the refused one is observable.
			writes.gotWS, writes.setCalled = uuid.Nil, false

			msg := fails(t, reg, member(), "inroad_contact_write", args)
			if !strings.Contains(msg, "rate limit") {
				t.Fatalf("refusal %q does not say it is a rate limit", msg)
			}
			if writes.gotWS != uuid.Nil || writes.setCalled {
				t.Fatal("the call over the limit still reached the writer")
			}
			if limiter.limit != agentWritesPerMinute {
				t.Fatalf("limit = %d, want %d", limiter.limit, agentWritesPerMinute)
			}
		})
	}
}

// The budget is per workspace and per client, and contact writes spend their
// own bucket rather than the CRM one.
func TestContactWriteLimitIsScopedToWorkspaceClientAndBucket(t *testing.T) {
	limiter := newCountingLimiter()
	reg := New(Deps{ContactWrites: &fakeContactWrites{}, CRM: &fakeCRM{}, WriteLimiter: limiter})
	args := contactWriteCalls()[methodLinkCompany]
	p := Principal{WorkspaceID: wsID, UserID: userID, Role: "member", AgentClientID: "mcp-client-1"}

	for i := 0; i < agentWritesPerMinute; i++ {
		ok(t, reg, p, "inroad_contact_write", args)
	}
	fails(t, reg, p, "inroad_contact_write", args)

	otherClient := p
	otherClient.AgentClientID = "mcp-client-2"
	ok(t, reg, otherClient, "inroad_contact_write", args)

	otherWorkspace := p
	otherWorkspace.WorkspaceID = otherWS
	ok(t, reg, otherWorkspace, "inroad_contact_write", args)

	// The exhausted contact budget leaves CRM writes alone.
	ok(t, reg, p, "inroad_company_write", `{"loading_message":"Creating company","method":"create","name":"Acme"}`)

	want := fmt.Sprintf("agent-contact-write:%s:mcp-client-1", wsID)
	if limiter.keys[0] != want {
		t.Fatalf("contact key = %q, want %q", limiter.keys[0], want)
	}
}

// First-party chat carries no agent client id, so the key falls back to the
// user — two users in one workspace do not share a budget.
func TestContactWriteLimitFallsBackToTheUser(t *testing.T) {
	limiter := newCountingLimiter()
	reg := New(Deps{ContactWrites: &fakeContactWrites{}, WriteLimiter: limiter})
	ok(t, reg, member(), "inroad_contact_write", contactWriteCalls()[methodUnlinkCompany])

	want := fmt.Sprintf("agent-contact-write:%s:%s", wsID, userID)
	if len(limiter.keys) != 1 || limiter.keys[0] != want {
		t.Fatalf("keys = %v, want [%s]", limiter.keys, want)
	}
}

func TestContactWriteLimiterFailsClosed(t *testing.T) {
	limiterErr := errors.New("redis unavailable")
	writes := &fakeContactWrites{}
	reg := New(Deps{ContactWrites: writes, WriteLimiter: &fakeRateLimiter{allowed: true, err: limiterErr}})

	res, err := reg.Execute(context.Background(), member(), "inroad_contact_write",
		json.RawMessage(contactWriteCalls()[methodLinkCompany]))
	if !errors.Is(err, limiterErr) || res.Success {
		t.Fatalf("result=%+v err=%v, want the limiter error to abort the call", res, err)
	}
	if writes.setCalled {
		t.Fatal("an unmetered write went through while the limiter was down")
	}
}

// Reads are never metered: a model resolving ids before a write must not burn
// the write budget doing it.
func TestContactReadIsNotMetered(t *testing.T) {
	limiter := newCountingLimiter()
	reg := New(Deps{Contacts: &fakeContacts{}, ContactWrites: &fakeContactWrites{}, WriteLimiter: limiter})
	ok(t, reg, member(), "inroad_contact_read", `{"loading_message":"Reading","method":"list"}`)

	if len(limiter.keys) != 0 {
		t.Fatalf("a read consulted the write limiter: %v", limiter.keys)
	}
}
