package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// agentWritesPerMinute caps one agent client's writes per workspace, per
// bucket. It bounds what a looping or prompt-injected model can do to tenant
// data before a human notices, without getting in the way of a real task.
const agentWritesPerMinute = 30

// writeBucket names one independently budgeted family of agent writes. Buckets
// are separate so a burst of contact edits cannot starve CRM writes (or the
// reverse) — and so adding a bucket leaves every existing counter untouched.
type writeBucket struct {
	// key is the Redis key segment; it must never change for an existing
	// bucket, or every in-flight window resets on deploy.
	key string
	// label names the bucket in the model-facing refusal.
	label string
}

var (
	crmWriteBucket     = writeBucket{key: "agent-crm-write", label: "CRM"}
	contactWriteBucket = writeBucket{key: "agent-contact-write", label: "contact"}
)

// withWriteLimit gates a write tool behind the shared fixed-window limiter.
// The key pins the workspace and the calling client (falling back to the user
// for first-party chat), so one tenant's or one client's traffic never spends
// another's budget. A nil limiter means the deployment has none to offer and
// the tool runs unlimited, exactly as before the limiter existed.
//
// It fails closed: a limiter error aborts the call rather than letting an
// unmetered write through while Redis is down.
func withWriteLimit(tool Tool, limiter RateLimiter, bucket writeBucket) Tool {
	if limiter == nil {
		return tool
	}
	execute := tool.Execute
	tool.Execute = func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
		clientID := p.AgentClientID
		if clientID == "" {
			clientID = p.UserID.String()
		}
		key := fmt.Sprintf("%s:%s:%s", bucket.key, p.WorkspaceID, clientID)
		allowed, err := limiter.Allow(ctx, key, agentWritesPerMinute, time.Minute)
		if err != nil {
			return Result{}, fmt.Errorf("limit %s agent writes: %w", bucket.label, err)
		}
		if !allowed {
			return Fail(bucket.label + " write rate limit reached; wait briefly before trying again"), nil
		}
		return execute(ctx, p, raw)
	}
	return tool
}
