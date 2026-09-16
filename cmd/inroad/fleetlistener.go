package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// The FLEET LISTENER: the control plane's machine-facing surface, serving the
// execution plane and nothing else.
//
// It is a SEPARATE listener from INROAD_HTTP_ADDR, never a mount on the API
// router — the same rule /metrics follows (httpx.MetricsMux) and for a sharper
// reason: one of these routes returns plaintext credentials and the other
// returns a tenant's compliance state, and neither is protected by a user
// session. What protects them is an operator binding this address where only
// the fleet can reach it, plus a shared bearer token. Off unless
// INROAD_FLEET_BROKER_ADDR is set, so a self-hosted install serves none of it.
//
// ONE LISTENER, ONE TOKEN, TWO TRANSPORTS — and the "one" is the decision worth
// recording, because the alternative is defensible and was considered.
//
// The two transports are the credential broker (internal/platform/credbroker,
// "open this mailbox's credential") and the coreapi remote transport
// (internal/coreapi/remote, "answer this coreapi method"). Separating them
// would give blast-radius isolation between "unwrap a credential" and "read job
// data": two tokens, so a leak of one does not grant the other.
//
// They share, for three reasons that all point the same way:
//
//  1. There is no principal that holds one and not the other. A role=send
//     worker needs BOTH at once — it brokers the credential it dials with and
//     it checks suppression before every send — so two tokens would partition
//     nothing. Two secrets with identical reach is not isolation; it is one
//     secret written down twice.
//  2. Two tokens is two rotations, and the second one is the one that gets
//     forgotten. credbroker's whole revocation story is "rotate one value
//     instead of re-encrypting every DEK"; a second value that must also be
//     rotated, on the same hosts, at the same time, weakens that rather than
//     strengthening it.
//  3. The asymmetry runs the wrong way for splitting. The credential token is
//     strictly the more powerful of the two — it yields the ability to
//     authenticate AS a customer's mailbox, which can send mail and read the
//     whole inbox. Guarding the weaker capability with a second secret while
//     handing the stronger one to the same host is ceremony, not containment.
//
// What would change the answer: a role that needs coreapi data and NOT
// credentials — a read-only analytics or reporting worker. That role does not
// exist, and when it does the split is additive rather than a rewrite, because
// both sides already take a base URL and a token as ordinary parameters.

// fleetDeps is what the fleet listener serves. Each field is the narrow
// interface its transport defined; the composition root supplies the same
// implementations the in-process paths use, so a fleet answer and a local one
// are produced by identical code rather than two that can drift.
type fleetDeps struct {
	// credentials opens a stored secret. cmd/inroad passes
	// inprocess.NewCredentialOpener — the SAME opener the in-process job builds
	// use.
	credentials credbroker.Opener
	// suppression answers one suppression question. cmd/inroad passes the
	// app/suppression store it already built for the campaign test-send check,
	// so there is exactly one implementation of "is this address suppressed"
	// behind the HTTP API, the in-process coreapi path and the wire.
	suppression remote.SuppressionReader
	// jobs answers the per-message job reads. cmd/inroad passes the SAME
	// in-process coreapi client the API server and its own workers use, by type
	// assertion — so a job a fleet worker is handed and a job the single-process
	// topology builds come out of identical code, including every workspace pin
	// and every gate, rather than two implementations that can drift.
	//
	// That build opens the mailbox credential as it always has. This transport
	// does not carry it: the job types tag their secrets json:"-" and a fleet
	// worker brokers them. See internal/coreapi/remote's jobs.go for why.
	jobs remote.JobReader
}

// newFleetHandler assembles the listener's router: each transport's own handler
// mounted under its own prefix, each enforcing the shared token itself.
//
// The prefixes come from the packages that own them rather than being spelled
// out here, so a route that moves cannot silently stop being mounted. Anything
// not under one of them is a 404: this listener is not a second copy of the API.
func newFleetHandler(d fleetDeps, token string, logger *slog.Logger) (http.Handler, error) {
	if d.credentials == nil || d.suppression == nil || d.jobs == nil {
		return nil, errors.New("fleet listener: every transport must be wired")
	}
	brokerHandler, err := credbroker.NewHandler(d.credentials, token, logger)
	if err != nil {
		return nil, err
	}
	coreHandler, err := remote.NewHandler(remote.Deps{Suppression: d.suppression, Jobs: d.jobs}, token, logger)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	// Mounted by prefix, NOT wrapped in http.StripPrefix: each inner mux
	// registers its routes at their full absolute path, so stripping would make
	// every one of them unreachable.
	mux.Handle(credbroker.PathPrefix, brokerHandler)
	mux.Handle(remote.PathPrefix, coreHandler)
	return mux, nil
}

// startFleetListener serves newFleetHandler on cfg.FleetBrokerAddr and returns
// the function that stops it. An empty address means the fleet listener is not
// served at all — the returned stop is then a no-op, and an installation that
// has not opted in has no such routes.
//
// Shutdown is a cancel-THEN-wait, like the metrics listener: a bare cancel
// unblocks httpx.Run's goroutine but nothing waits for its own graceful
// srv.Shutdown, so run() (and the process) can return before the listener has
// actually stopped.
func startFleetListener(ctx context.Context, cfg *config.Config, d fleetDeps, logger *slog.Logger) (func(), error) {
	if cfg.FleetBrokerAddr == "" {
		return func() {}, nil
	}
	h, err := newFleetHandler(d, cfg.FleetBrokerToken, logger)
	if err != nil {
		return nil, err
	}
	srvCtx, cancel := context.WithCancel(ctx)
	srv := httpx.NewServer(cfg.FleetBrokerAddr, h)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpx.Run(srvCtx, srv); err != nil {
			logger.Error("fleet listener error", "err", err)
		}
	}()
	logger.Info("fleet listener listening", "addr", cfg.FleetBrokerAddr,
		"transports", "credentials, coreapi",
		"note", "serves decrypted mailbox credentials and coreapi answers to fleet workers; restrict this address to the fleet network")
	return func() {
		cancel()
		wg.Wait()
	}, nil
}
