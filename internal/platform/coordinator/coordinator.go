// Package coordinator is the pairing seam of warmup: the boundary between "which
// mailbox should this one warm up with" and "who is allowed to answer that".
//
// It exists for the same reason coreapi does. Today the only answer comes from the
// workspace's own participant table, in-process; the design
// (docs/warmup-reputation-network-design.md §9) anticipates a shared or federated
// pool run as a SEPARATE service with its own data store — explicitly NOT a
// cross-workspace SQL query added to the Inroad database. Naming the boundary now
// means that later coordinator is an adapter behind this interface rather than a
// rewrite of the send path.
//
// Two rules make the difference between a seam and a leak, and both are structural
// here rather than documentary:
//
//   - Nothing in this package can express "give me a partner from another
//     workspace". A PairRequest names exactly one workspace — the caller's own —
//     and LocalCoordinator refuses any candidate whose workspace differs
//     (ErrCrossTenant), the same belt-and-braces coreapi keeps behind its own SQL
//     pins.
//   - A participant's mail ADDRESS is never published. It appears only in an
//     Assignment, only to the one peer that is about to send to it, and only for
//     the life of one lease. An Advertisement that carried addresses would be a
//     harvestable directory of every mailbox in the pool.
//
// The trust questions a cross-workspace pool raises — and what would have to become
// true before one is switched on — are security.md invariant 62, which is committed.
// The field-by-field derivation of "minimum routing data" is in
// docs/superpowers/specs/2026-08-27-warmup-coordinator-seam-design.md, which is NOT:
// that directory is gitignored, so treat invariant 62 and the comments on each payload
// type below as the durable record.
package coordinator

import (
	"context"
	"errors"
	"time"

	"github.com/inroad/inroad/internal/platform/warmup"
)

// Coordinator answers one question — who should this mailbox warm up with — and
// issues the authority to act on the answer.
//
// Deliberately two methods. Every method has to be expressible by the local
// implementation WITHOUT a network, or it is a remote feature wearing an interface:
// a third method that only a remote could mean would be a stub locally, and a stub
// on this seam is how the tenancy rules above stop being enforced in the case that
// actually ships.
//
// Reporting an outcome is NOT here on purpose. §9 routes the worker's outcome
// report back through coreapi, which already records it; a coordinator method for
// it would be a no-op locally and a second, divergent write path remotely.
type Coordinator interface {
	// Advertise publishes one opted-in participant to the pool this coordinator
	// serves and returns whether the pool admits it. It carries no address (see
	// the package doc) and no evidence: admission is a lane and consent question,
	// not a health re-litigation.
	Advertise(ctx context.Context, ad Advertisement) (Admission, error)

	// RequestPair asks for one partner for one send, and returns the minimum
	// routing data plus the lease that authorizes it. ErrNoPartner — a refusal,
	// not a failure — when the pool has nobody eligible.
	RequestPair(ctx context.Context, req PairRequest) (Assignment, error)
}

// Errors this seam speaks. All four are sentinels so a caller branches with
// errors.Is instead of matching on a message: a refusal that has to be
// string-matched becomes a refusal that gets ignored.
var (
	// ErrNoPartner means the pool holds nobody this participant may pair with
	// right now. It is the ordinary state of a one-mailbox workspace and of any
	// pool whose members are all inside their pair cooldown, so callers must
	// treat it as "skip this tick", never as a failure to retry or alert on.
	ErrNoPartner = errors.New("coordinator: no eligible partner")

	// ErrCrossTenant means a candidate carried a workspace other than the
	// requester's. Impossible against a correctly pinned local pool, which is
	// exactly why the check is here: it is what still fails closed when a future
	// refactor relaxes the pin, and it is the check a shared pool would have to
	// defeat rather than merely omit. Mirrors coreapi.ErrCrossTenant.
	ErrCrossTenant = errors.New("coordinator: cross-tenant partner rejected")

	// ErrInvalidRequest means the CALLER's own input is unusable — no workspace,
	// no participant id, no clock, or a lane that may not send at all. It is
	// loud rather than silently downgraded to ErrNoPartner because containment is
	// decided before this call, and a quarantined mailbox arriving here means the
	// gate upstream did not run.
	ErrInvalidRequest = errors.New("coordinator: invalid request")

	// ErrInvalidCandidate means the POOL answered with something that may not be
	// handed out: a missing id or address, a lane the requester may not pair
	// with, or the requester itself. Distinct from ErrNoPartner because "the pool
	// is empty" and "the pool is wrong" need different reactions from an operator.
	ErrInvalidCandidate = errors.New("coordinator: pool returned an unusable candidate")
)

// Scope is the pool a participant consents to join. There is no default: an
// advertisement must state its scope, because the one thing §14 rules out
// absolutely is a mailbox ending up in a shared network without being put there
// deliberately, and a zero value that means "workspace" today is a zero value that
// could mean something else after a refactor.
type Scope string

const (
	// ScopeWorkspace pairs only with the workspace's own mailboxes. Today's
	// behaviour, and the only scope any shipped coordinator admits.
	ScopeWorkspace Scope = "workspace"
	// ScopeShared pairs with mailboxes belonging to other tenants. No
	// implementation admits this yet; see the spec's sentinel-capacity section
	// for what must be true first.
	ScopeShared Scope = "shared"
)

// Participant identifies one warmup mailbox to a coordinator and states the two
// facts a pairing decision may use. It is the whole of what a coordinator knows
// about a member — no address, no rates, no history.
//
// ID is opaque to the coordinator. Locally it is the mailbox UUID, which never
// leaves the workspace. A remote adapter substitutes a pseudonym and keeps the
// mapping on OUR side of the boundary (§9: the coordinator stores pseudonymous
// participant ids), so the coordinator cannot name a customer's mailbox even if
// its own store is dumped.
type Participant struct {
	WorkspaceID string
	ID          string
	// Lane is the pool lane (warmup.Lane*). It is required: an empty lane
	// normalizes to probation inside warmup, and a coordinator inferring a lane
	// is a coordinator making a containment decision it has no evidence for.
	Lane string
	// IsSentinel marks an operator-controlled measurement mailbox, the only
	// member that may pair across lanes (warmup.Pairable).
	IsSentinel bool
}

// poolFacts is the participant as the pairing rule sees it. The rule itself lives
// in warmup and is never restated here — a second opinion about who may pair with
// whom is the defect this subsystem keeps having.
func (p Participant) poolFacts() warmup.Participant {
	return warmup.Participant{Lane: p.Lane, IsSentinel: p.IsSentinel}
}

// Advertisement is what one workspace publishes about one opted-in participant.
//
// This is the payload that crosses the trust boundary in the OUTBOUND direction,
// so its field list is the security-relevant part of this package. It carries
// exactly the participant identity above and the scope consented to. Adding a
// field here publishes it about every member of the pool, to every peer, for as
// long as the membership lasts — which is a different and much weaker disclosure
// than an Assignment's, and is why capacity, fault domains and coverage gaps are
// discussed in the spec rather than declared here: none of them has a consumer
// yet, and an unread field is a leak with no benefit.
type Advertisement struct {
	Participant Participant
	Scope       Scope
}

// Admission is a pool's verdict on an advertisement. A refusal is a normal answer
// — a quarantined mailbox SHOULD be refused — so it is a reason code rather than
// an error, the same shape warmup.LeaseValid uses for a refused lease.
type Admission struct {
	Admitted   bool
	ReasonCode string
}

// Admission reason codes.
const (
	AdmissionOK = ""
	// AdmissionLaneMayNotSend: the participant's lane originates no traffic
	// (pending_auth, quarantine, blocked). Containment stays warmup.LaneMaySend's
	// decision; this reports it, it does not re-derive it.
	AdmissionLaneMayNotSend = "lane_may_not_send"
	// AdmissionSharedPoolUnavailable: the participant consented to a shared pool
	// and this coordinator has none. Refused rather than quietly downgraded to
	// the local pool: the downgrade is safe (strictly less exposure) but it would
	// leave an operator believing a membership is live when nothing implements
	// it, and a consent signal nobody acts on is the kind that gets acted on
	// later by accident.
	AdmissionSharedPoolUnavailable = "shared_pool_unavailable"
)

// Constraints are the allocation policy the CALLER already computed — today's
// pair cooldown and per-pair daily budget, exactly as SelectWarmupPartner takes
// them. They travel with the request rather than living in the coordinator so the
// local pool keeps one home for the arithmetic.
//
// A coordinator may TIGHTEN these and may never loosen them. A remote pool has its
// own caps and will apply the stricter of the two; it must not hand back a pair
// that the requester's own policy would have refused.
type Constraints struct {
	// CooldownSince is the earliest last-paired time still acceptable: a partner
	// paired with more recently is skipped unless nobody else is eligible.
	CooldownSince time.Time
	// MaxPairSendsPerDay caps what one pair may exchange in a day, symmetrically.
	MaxPairSendsPerDay int
}

// PairRequest asks for one partner for one send.
//
// Note what cannot be said here: there is no pool selector, no peer workspace, no
// "partner lane" preference. The only workspace in the type is the requester's
// own, so the local implementation cannot be ASKED for a foreign partner — it does
// not merely refuse to, which is the difference between an invariant and a check
// someone can delete.
type PairRequest struct {
	Requester   Participant
	Constraints Constraints
	// Now is the requester's clock, passed rather than read so the whole path
	// stays deterministic and testable (the convention every warmup policy
	// function follows). A remote coordinator uses its own clock and may return
	// an EARLIER expiry than this one implies; it may never return a later one.
	Now time.Time
}

// Partner is the minimum routing data: everything the sender needs to deliver one
// warmup message, and nothing else.
//
// Two fields, and each is irreducible. ID is the handle the pair cooldown, the
// thread and the receipt all key on — without a stable reference the same peer
// cannot be recognized on the next tick. Address is the RFC5321 recipient; §9
// concedes it is unavoidable that cross-tenant warmup exposes the two addresses to
// the two participating mail systems, and it is the ONLY thing about a peer that
// this is true of.
//
// What is deliberately absent, because a sender does not need it to send:
// the partner's display name (a person's name, and the local caller already has
// its own mailbox's), its lane or health (which would publish another tenant's
// standing to a stranger), its provider, and its workspace.
type Partner struct {
	ID      string
	Address string
}

// Lease is the authority to perform one send to one partner.
//
// Terms is warmup's existing lease, unchanged, so the claim path validates a
// coordinator-issued lease with warmup.LeaseValid and nothing new has to agree
// with anything. That is also the honest statement of how a LOCAL lease is
// verified: by re-reading the sender's current lane at claim time, not by checking
// a signature. A remote coordinator cannot be verified that way — see the spec's
// section on what a signed lease would have to add; this package does not carry an
// unverified signature field, because a signature nothing checks is worse than
// none.
type Lease struct {
	// ID is derived from the request, not random: the same request produces the
	// same lease, so a retried tick cannot double-book a pair. Retries are the
	// normal case in an asynq-driven path.
	ID    string
	Terms warmup.Lease
}

// Assignment is one answer: who to mail, and the authority to mail them.
type Assignment struct {
	Partner Partner
	Lease   Lease
}
