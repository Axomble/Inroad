// Package audit is the write side of the workspace audit log: the event
// vocabulary, the actor model, the request-metadata context, and the one
// function that persists an event.
//
// It lives in platform/ rather than app/ because EVERY domain records into it
// and app/* packages may not import each other. A domain depends on this
// package's small Recorder interface (best-effort events) or calls Insert with
// its own transaction-bound queries (events that must commit or fail with the
// action); the composition root wires the Postgres implementation. The read
// side — the owner/admin viewer — is internal/app/audit.
//
// FAILURE POLICY (security.md invariant 82). Two classes, chosen per action:
//
//   - IN-TRANSACTION (fail the action): events that grant or remove authority —
//     api key created/revoked, member invited/invite revoked/role changed. The
//     audit row is written through the same transaction as the change, so an
//     authority change without its record cannot exist. data.exported is also
//     fail-closed, but because it precedes a disclosure rather than sharing a
//     transaction: the export does not start if the record cannot be written.
//   - BEST-EFFORT (log and continue): sign-ins, sign-in failures, mailbox
//     connect/disconnect, campaign state, settings. An audit outage must not
//     lock every user out, or stop an operator pausing a campaign that is
//     burning a domain. A failed write is logged at ERROR with the action and
//     workspace so the gap is visible, never silent.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Action is a stable dotted event name. Stored verbatim and filtered by
// prefix, so a published name is never renamed — add a new one instead.
type Action string

// The action vocabulary. Grouped by the prefix the viewer filters on.
const (
	ActionAuthLogin       Action = "auth.login"
	ActionAuthLoginFailed Action = "auth.login_failed"

	ActionMemberInvited       Action = "member.invited"
	ActionMemberInviteRevoked Action = "member.invite_revoked"
	ActionMemberJoined        Action = "member.joined"
	ActionMemberRoleChanged   Action = "member.role_changed"

	ActionMailboxConnected    Action = "mailbox.connected"
	ActionMailboxDisconnected Action = "mailbox.disconnected"
	ActionMailboxPaused       Action = "mailbox.paused"
	ActionMailboxResumed      Action = "mailbox.resumed"

	ActionCampaignStarted Action = "campaign.started"
	ActionCampaignPaused  Action = "campaign.paused"
	ActionCampaignResumed Action = "campaign.resumed"

	ActionAPIKeyCreated Action = "apikey.created"
	ActionAPIKeyRevoked Action = "apikey.revoked"

	ActionDataExported Action = "data.exported"

	ActionSettingsChanged Action = "settings.changed"
)

// AllActions is every action this server records, in display order. It is the
// source of truth the OpenAPI enum is checked against.
var AllActions = []Action{
	ActionAuthLogin, ActionAuthLoginFailed,
	ActionMemberInvited, ActionMemberInviteRevoked, ActionMemberJoined, ActionMemberRoleChanged,
	ActionMailboxConnected, ActionMailboxDisconnected, ActionMailboxPaused, ActionMailboxResumed,
	ActionCampaignStarted, ActionCampaignPaused, ActionCampaignResumed,
	ActionAPIKeyCreated, ActionAPIKeyRevoked,
	ActionDataExported,
	ActionSettingsChanged,
}

// IsKnownAction reports whether a is in the vocabulary.
func IsKnownAction(a Action) bool { return slices.Contains(AllActions, a) }

// ActorType is what kind of principal acted.
type ActorType string

// The actor kinds. Mirrored by the CHECK constraint on audit_events.actor_type.
const (
	ActorUser        ActorType = "user"
	ActorAPIKey      ActorType = "api_key"
	ActorOAuthClient ActorType = "oauth_client"
	ActorAgent       ActorType = "agent"
	ActorSystem      ActorType = "system"
)

// AllActorTypes is every actor kind, for validation at the read boundary.
var AllActorTypes = []ActorType{ActorUser, ActorAPIKey, ActorOAuthClient, ActorAgent, ActorSystem}

// IsKnownActorType reports whether t is an actor kind.
func IsKnownActorType(t ActorType) bool { return slices.Contains(AllActorTypes, t) }

// Actor is who performed an action.
type Actor struct {
	Type ActorType
	// ID identifies the principal within Type: a user id, an api key id, an
	// OAuth client id, an agent run (or client) id, or a system component name.
	ID string
	// UserID is the human on whose authority the actor acted — the user
	// themself, an api key's creator, the user who delegated to an agent. Nil
	// for a system actor.
	UserID *uuid.UUID
}

// UserActor is the actor for a human acting directly.
func UserActor(userID uuid.UUID) Actor {
	return Actor{Type: ActorUser, ID: userID.String(), UserID: &userID}
}

// SystemActor is the actor for work no principal initiated (a worker sweep,
// the operator CLI, an OAuth callback that carries no user).
func SystemActor(component string) Actor {
	return Actor{Type: ActorSystem, ID: component}
}

// Metadata is small, flat, string-valued context for an event. Flat strings
// only, by construction: a nested object is how a message body or a whole
// request ends up in a security log.
type Metadata map[string]string

// Event is one audit record before it is persisted.
type Event struct {
	WorkspaceID uuid.UUID
	Actor       Actor
	Action      Action
	// TargetType/TargetID name the object acted on ("campaign", its id). Both
	// empty when the action has no object beyond the workspace.
	TargetType string
	TargetID   string
	Metadata   Metadata
	// IP and UserAgent come from the request (WithRequest) when not set here.
	IP        string
	UserAgent string
}

// Bounds enforced before a write. They match (or undercut) the table's CHECK
// constraints so a violation is a clear Go error, not a Postgres one.
const (
	maxMetadataKeys     = 16
	maxMetadataValueLen = 512
	maxUserAgentLen     = 512
	maxIDLen            = 200
	maxTargetTypeLen    = 50
	// maxMetadataBytes keeps the encoded object comfortably under the table's
	// 4096-byte pg_column_size CHECK, whatever jsonb's own overhead.
	maxMetadataBytes = 3072
)

var metadataKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// forbiddenKeyParts are substrings a metadata key may never contain. A
// BACKSTOP, not the guarantee: the guarantee is that every call site passes
// ids, names, counts and enums. This catches the lazy mistake of passing a
// credential or body through under its own name.
var forbiddenKeyParts = []string{
	"password", "passwd", "secret", "token", "credential", "ciphertext",
	"authorization", "cookie", "body", "otp", "private",
}

// ErrInvalidEvent is returned for an event that must not be written.
var ErrInvalidEvent = errors.New("invalid audit event")

// Validate checks ev against the vocabulary and the metadata rules.
func Validate(ev Event) error {
	switch {
	case ev.WorkspaceID == uuid.Nil:
		return fmt.Errorf("%w: workspace_id is required", ErrInvalidEvent)
	case !IsKnownAction(ev.Action):
		return fmt.Errorf("%w: unknown action %q", ErrInvalidEvent, ev.Action)
	case !IsKnownActorType(ev.Actor.Type):
		return fmt.Errorf("%w: unknown actor type %q", ErrInvalidEvent, ev.Actor.Type)
	case len(ev.Actor.ID) > maxIDLen, len(ev.TargetID) > maxIDLen:
		return fmt.Errorf("%w: id longer than %d", ErrInvalidEvent, maxIDLen)
	case len(ev.TargetType) > maxTargetTypeLen:
		return fmt.Errorf("%w: target_type longer than %d", ErrInvalidEvent, maxTargetTypeLen)
	case len(ev.Metadata) > maxMetadataKeys:
		return fmt.Errorf("%w: more than %d metadata keys", ErrInvalidEvent, maxMetadataKeys)
	}
	for k, v := range ev.Metadata {
		if !metadataKeyPattern.MatchString(k) {
			return fmt.Errorf("%w: metadata key %q is not snake_case", ErrInvalidEvent, k)
		}
		for _, part := range forbiddenKeyParts {
			if strings.Contains(k, part) {
				return fmt.Errorf("%w: metadata key %q looks like a secret or content field", ErrInvalidEvent, k)
			}
		}
		if len(v) > maxMetadataValueLen {
			return fmt.Errorf("%w: metadata value for %q longer than %d", ErrInvalidEvent, k, maxMetadataValueLen)
		}
	}
	return nil
}

// New builds an event attributed from ctx: the actor WithActor stored (system
// when none), and the IP / user agent WithRequest stored.
func New(ctx context.Context, ws uuid.UUID, action Action, targetType, targetID string, md Metadata) Event {
	actor, ok := ActorFrom(ctx)
	if !ok {
		actor = SystemActor("")
	}
	ip, ua := RequestFrom(ctx)
	return Event{
		WorkspaceID: ws, Actor: actor, Action: action,
		TargetType: targetType, TargetID: targetID, Metadata: md,
		IP: ip, UserAgent: ua,
	}
}

// Insert validates ev and writes it through q. Pass a transaction-bound
// *gen.Queries (gen.New(tx)) to couple the audit row to the action's own
// transaction; this is how the in-transaction class of the failure policy is
// implemented.
func Insert(ctx context.Context, q *gen.Queries, ev Event) error {
	if err := Validate(ev); err != nil {
		return err
	}
	params, err := insertParams(ev)
	if err != nil {
		return err
	}
	if err := q.InsertAuditEvent(ctx, params); err != nil {
		return fmt.Errorf("audit: insert %s: %w", ev.Action, err)
	}
	return nil
}

func insertParams(ev Event) (gen.InsertAuditEventParams, error) {
	md := ev.Metadata
	if md == nil {
		md = Metadata{}
	}
	raw, err := json.Marshal(md)
	if err != nil {
		return gen.InsertAuditEventParams{}, fmt.Errorf("audit: marshal metadata: %w", err)
	}
	if len(raw) > maxMetadataBytes {
		return gen.InsertAuditEventParams{}, fmt.Errorf("%w: metadata encodes to %d bytes, limit %d", ErrInvalidEvent, len(raw), maxMetadataBytes)
	}
	var userID pgtype.UUID
	if ev.Actor.UserID != nil {
		userID = pgtype.UUID{Bytes: *ev.Actor.UserID, Valid: true}
	}
	return gen.InsertAuditEventParams{
		WorkspaceID: ev.WorkspaceID,
		ActorType:   string(ev.Actor.Type),
		ActorID:     ev.Actor.ID,
		ActorUserID: userID,
		Action:      string(ev.Action),
		TargetType:  ev.TargetType,
		TargetID:    ev.TargetID,
		Ip:          parseIP(ev.IP),
		UserAgent:   truncate(ev.UserAgent, maxUserAgentLen),
		Metadata:    raw,
	}, nil
}

// parseIP maps a textual address to the nullable INET column. Anything
// unparseable is stored as NULL rather than failing the write: the address is
// context, and a proxy handing us garbage must not cost the record.
func parseIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}

// truncate cuts s to at most n bytes on a rune boundary. A user agent is
// caller-controlled; truncating keeps an oversized one from failing the write.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Recorder persists an event outside any caller transaction.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// PgRecorder is the Postgres Recorder.
type PgRecorder struct{ q *gen.Queries }

// NewPgRecorder builds a Recorder over db (a pool, or a tx).
func NewPgRecorder(db gen.DBTX) *PgRecorder { return &PgRecorder{q: gen.New(db)} }

// Record implements Recorder.
func (r *PgRecorder) Record(ctx context.Context, ev Event) error { return Insert(ctx, r.q, ev) }

// bestEffortTimeout bounds a best-effort write. It runs detached from the
// request's cancellation (see Emit), so it needs a deadline of its own.
const bestEffortTimeout = 5 * time.Second

// Emit records ev through rec on the BEST-EFFORT path: it never returns an
// error, and a failure is logged at ERROR with the action and workspace. A nil
// Recorder is "audit not wired" and is a no-op, so a service constructed
// without one (most unit tests) behaves exactly as before.
//
// The write is detached from ctx's cancellation (context.WithoutCancel keeps
// its values — trace ids, request metadata) because Emit runs AFTER the action
// committed: a client that disconnects the instant its campaign pauses must not
// thereby erase the record that it paused it.
func Emit(ctx context.Context, rec Recorder, ev Event) {
	if rec == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bestEffortTimeout)
	defer cancel()
	if err := rec.Record(wctx, ev); err != nil {
		slog.ErrorContext(ctx, "audit event not recorded",
			"action", string(ev.Action), "workspace_id", ev.WorkspaceID.String(), "err", err)
	}
}
