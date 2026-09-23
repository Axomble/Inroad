package remote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// The methods this transport deliberately DOES NOT CARRY, and the refusal that
// is the point of the file.
//
// # Why these exist at all
//
// After slice 4 a role=send worker's coreapi.Client IS this type: cmd/worker
// opens no pool and builds no inprocess client, so *Client must satisfy the
// whole interface. Six of coreapi.Client's methods have no remote
// implementation and never will, and that is a design decision rather than an
// unfinished slice.
//
// # Why they will never have one
//
// Every one of them is a CROSS-TENANT SCAN. ListDueEnrollments enumerates every
// workspace's due enrollments; ListActiveMailboxes and ListDueWarmupMailboxes
// enumerate every workspace's mailboxes; ListStaleSendingDomains enumerates
// every domain the deployment sends from; EvaluateWarmupHealth recomputes every
// participant's health. A route that answered any of them would be a route that
// returns "rows matching X" over the tenant database — the exact capability
// docs/security.md invariant 73 says this seam may never express, and the exact
// capability the plane split exists to take away. They run on the control role,
// beside the database, on infrastructure the operator already trusts with it
// (internal/worker.registerScheduled).
//
// RecordSendingDomainAuth is a WRITE rather than a scan, and it is here for a
// different reason: it is the write-back half of ListStaleSendingDomains and
// runs in the same sweep handler. A remote implementation would be a route with
// no caller.
//
// # Why a refusal instead of a nil method or a panic
//
// A role=send worker never reaches any of these — internal/worker.Register
// gates registerScheduled on Role.RunsScheduledWork(), which is false for
// RoleSend — so in a correct deployment none of them is called. This file is
// what happens if that ever stops being true. A nil-returning stub would report
// "no work due" and the sweep would quietly do nothing; a panic would be
// recovered by asynq and retried until the task exhausted. An error that names
// the method and says why is the only one of the three an operator can act on.
//
// The same reasoning does NOT extend to the optional capability interfaces
// (maintenance.Cleaner, maintenance.Retainer, deliverability.Breaker, recipientesp.Core,
// fleet.Rotator, jobrun.Recorder). Those are consumed through comma-ok type
// assertions, so NOT implementing them is already an explicit, handled answer —
// the registrar logs and skips. Implementing them here to return an error would
// convert a handled absence into a runtime failure, which is strictly worse.

// ErrControlPlaneOnly refuses a method that has no remote transport by design.
//
// It is deliberately one sentinel rather than six: nothing branches on WHICH
// method was refused, and every caller is a control-role sweep that should
// never have reached a fleet worker in the first place. The wrapped message
// names the method, so the log line is specific even though the sentinel is not.
var ErrControlPlaneOnly = errors.New(
	"coreapi remote: this method is control-plane only and has no remote transport: it scans across tenants, which this seam may never express (docs/security.md invariant 73)")

// controlPlaneOnly builds the refusal for one named method.
func controlPlaneOnly(method string) error {
	return fmt.Errorf("%s: %w", method, ErrControlPlaneOnly)
}

// ListDueEnrollments enumerates every workspace's due enrollments. Control
// plane only — see this file's doc.
func (c *Client) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return nil, controlPlaneOnly("ListDueEnrollments")
}

// ListActiveMailboxes enumerates every workspace's pollable mailboxes. Control
// plane only.
func (c *Client) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, controlPlaneOnly("ListActiveMailboxes")
}

// ListDueWarmupMailboxes enumerates every workspace's warming mailboxes.
// Control plane only.
func (c *Client) ListDueWarmupMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, controlPlaneOnly("ListDueWarmupMailboxes")
}

// EvaluateWarmupHealth recomputes every enabled participant's health state.
// Control plane only.
func (c *Client) EvaluateWarmupHealth(context.Context) error {
	return controlPlaneOnly("EvaluateWarmupHealth")
}

// ListStaleSendingDomains enumerates every domain the deployment sends from.
// Control plane only.
func (c *Client) ListStaleSendingDomains(context.Context, time.Duration) ([]coreapi.SendingDomainRef, error) {
	return nil, controlPlaneOnly("ListStaleSendingDomains")
}

// RecordSendingDomainAuth persists one completed domain check. Control plane
// only: it is the write-back half of ListStaleSendingDomains and runs in the
// same sweep.
func (c *Client) RecordSendingDomainAuth(context.Context, coreapi.SendingDomainAuth) error {
	return controlPlaneOnly("RecordSendingDomainAuth")
}

// THE compile-time assertion this whole slice turns on.
//
// *Client is now a complete coreapi.Client, which is what lets cmd/worker hand
// a role=send worker this type INSTEAD of an inprocess client built over a nil
// pool. That distinction is the security property: a type with no *pgxpool.Pool
// field cannot dereference one, so "this worker cannot reach the database" is a
// fact about the type rather than a claim about which code paths happen to be
// unreachable today.
var _ coreapi.Client = (*Client)(nil)
