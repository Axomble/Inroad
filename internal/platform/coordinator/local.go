package coordinator

import (
	"context"
	"fmt"

	"github.com/inroad/inroad/internal/platform/warmup"
)

// Pool is the caller's own workspace pool of warmup participants.
//
// Consumer-defined and deliberately one method: the control plane already owns
// partner selection (SelectWarmupPartner — recency spread, symmetric pair budget,
// lane and sentinel eligibility, workspace pin), and reimplementing that ordering
// here would create the second source of truth every repeated defect in this
// subsystem has come from. LocalCoordinator adds the seam's rules on top; it does
// not choose differently.
type Pool interface {
	// SelectPartner returns one eligible partner for req.Requester from that
	// requester's OWN workspace.
	//
	// found=false means nobody is eligible, which is not an error (pgx.ErrNoRows
	// from the selection query maps to it).
	SelectPartner(ctx context.Context, req PairRequest) (c Candidate, found bool, err error)
}

// Candidate is a pool's answer before the seam has vetted it: a participant plus
// the address that makes it mailable.
//
// WorkspaceID must be read from the candidate's OWN row, never copied from the
// request. Copying it makes the ErrCrossTenant check assert that a value equals
// itself, which is a check that passes forever and protects nothing.
type Candidate struct {
	Participant
	Address string
}

// LocalCoordinator is the workspace-local pool: today's behaviour, and the default
// for every self-hosted install. It is not a placeholder for a networked one — a
// pool of one workspace's own mailboxes is a complete and correct warmup pool, and
// §9 requires it to keep working with no coordinator service present.
type LocalCoordinator struct {
	pool Pool
}

var _ Coordinator = (*LocalCoordinator)(nil)

// NewLocal returns the local coordinator over the caller's own pool.
func NewLocal(pool Pool) *LocalCoordinator { return &LocalCoordinator{pool: pool} }

// Advertise admits a participant to the workspace-local pool.
//
// There is nothing to publish TO: the local pool's members are the workspace's own
// participant rows, which the control plane already holds. What the call does is
// the half that a remote coordinator would also do, and the half that matters —
// decide whether this participant belongs in the pool it named. Keeping that
// decision on this side of the seam is what makes the remote one an adapter: it
// answers the same question over the wire instead of a different question locally.
func (c *LocalCoordinator) Advertise(_ context.Context, ad Advertisement) (Admission, error) {
	if err := vetParticipant(ad.Participant); err != nil {
		return Admission{}, err
	}
	switch ad.Scope {
	case ScopeWorkspace:
		// The pool this coordinator serves; carry on to the lane check.
	case ScopeShared:
		// The only shared pool that exists is the one in the design document.
		// Answering "admitted" here — or silently pairing them locally — would
		// be the coordinator telling an operator their network membership is
		// live.
		return Admission{ReasonCode: AdmissionSharedPoolUnavailable}, nil
	default:
		return Admission{}, fmt.Errorf("%w: scope %q is not a pool", ErrInvalidRequest, ad.Scope)
	}
	if !warmup.LaneMaySend(ad.Participant.Lane) {
		return Admission{ReasonCode: AdmissionLaneMayNotSend}, nil
	}
	return Admission{Admitted: true, ReasonCode: AdmissionOK}, nil
}

// RequestPair returns one partner from the requester's OWN workspace, and the
// lease that authorizes one send to it.
//
// The order is load-bearing. The requester is vetted before the pool is touched,
// so a mailbox that may not send is never described to a coordinator at all — a
// local pool would merely waste a query, but a remote one would have been told
// which of our mailboxes is in trouble, and it has no business knowing.
func (c *LocalCoordinator) RequestPair(ctx context.Context, req PairRequest) (Assignment, error) {
	if err := vetRequest(req); err != nil {
		return Assignment{}, err
	}
	cand, found, err := c.pool.SelectPartner(ctx, req)
	if err != nil {
		return Assignment{}, fmt.Errorf("coordinator: select partner: %w", err)
	}
	if !found {
		return Assignment{}, ErrNoPartner
	}
	if err := vetCandidate(req.Requester, cand); err != nil {
		return Assignment{}, err
	}
	return Assignment{
		Partner: Partner{ID: cand.ID, Address: cand.Address},
		Lease:   issueLease(req, cand.ID),
	}, nil
}

// vetRequest rejects a request this coordinator must not act on. Every case is the
// CALLER's own state, which is why it is an error and not ErrNoPartner: warmup's
// containment gates run upstream, and a request that fails here means one of them
// did not.
func vetRequest(req PairRequest) error {
	if err := vetParticipant(req.Requester); err != nil {
		return err
	}
	switch r := req.Requester; {
	case req.Now.IsZero():
		return fmt.Errorf("%w: no clock", ErrInvalidRequest)
	case !warmup.LaneMaySend(r.Lane):
		// A lane change racing this call lands here too. The sweep runs again in
		// minutes, and failing loudly on a rare race beats being quiet about the
		// common cause, which is a missing gate.
		return fmt.Errorf("%w: participant %q is in lane %q and may not send", ErrInvalidRequest, r.ID, r.Lane)
	}
	return nil
}

// vetParticipant rejects a participant that identifies nothing. Shared by both
// calls: a member the coordinator cannot name, or whose lane it would have to
// guess, is unusable whether it is joining the pool or asking it for a partner.
func vetParticipant(p Participant) error {
	switch {
	case p.WorkspaceID == "":
		return fmt.Errorf("%w: no workspace", ErrInvalidRequest)
	case p.ID == "":
		return fmt.Errorf("%w: no participant id", ErrInvalidRequest)
	case p.Lane == "":
		return fmt.Errorf("%w: participant %q has no lane", ErrInvalidRequest, p.ID)
	}
	return nil
}

// vetCandidate rejects a pool answer that must not become an assignment.
//
// The tenancy check is first and does not depend on the rest: a foreign candidate
// is the one failure here that is a breach rather than a bug, and it must be named
// as one even when the row is also malformed.
func vetCandidate(requester Participant, c Candidate) error {
	switch {
	case c.WorkspaceID != requester.WorkspaceID:
		return fmt.Errorf("%w: partner %q belongs to another workspace", ErrCrossTenant, c.ID)
	case c.ID == "":
		return fmt.Errorf("%w: partner has no id", ErrInvalidCandidate)
	case c.Address == "":
		return fmt.Errorf("%w: partner %q has no address", ErrInvalidCandidate, c.ID)
	case c.Lane == "":
		// Never defaulted. An empty lane normalizes to probation inside warmup,
		// so guessing here would silently decide a containment question.
		return fmt.Errorf("%w: partner %q has no lane", ErrInvalidCandidate, c.ID)
	case c.ID == requester.ID:
		return fmt.Errorf("%w: partner %q is the requester", ErrInvalidCandidate, c.ID)
	case !warmup.Pairable(requester.poolFacts(), c.poolFacts()):
		return fmt.Errorf("%w: lane %q may not pair with lane %q", ErrInvalidCandidate, requester.Lane, c.Lane)
	}
	return nil
}
