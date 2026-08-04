package campaign

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
)

// Preflight check ids. The exact strings are the wire contract (GET
// /campaigns/{id}/preflight): the frontend keys UI copy/icons off them.
const (
	CheckSequenceSteps   = "sequence_steps"
	CheckEmptyBodies     = "empty_bodies"
	CheckScheduleWindows = "schedule_windows"
	CheckSenderPool      = "sender_pool"
	CheckAudience        = "audience"
	CheckDomainAuth      = "domain_auth"
	CheckTracking        = "tracking"
	CheckDailyLimit      = "daily_limit"
	CheckWarmupHealth    = "warmup_health"
)

// Preflight check severities.
const (
	SeverityPass = "pass"
	SeverityWarn = "warn"
	SeverityFail = "fail"
)

// PreflightCheck is one readiness check's verdict.
type PreflightCheck struct {
	ID       string
	Severity string // "pass" | "warn" | "fail"
	Title    string
	Detail   string
	Remedy   string
}

// PreflightReport is GET /campaigns/{id}/preflight: every check, plus the
// aggregate Ready flag (true iff no check is severity fail).
type PreflightReport struct {
	Ready  bool
	Checks []PreflightCheck
}

// PreflightStep is the slice of a sequence step ComputePreflight needs: just
// enough to detect an empty body, decoupled from the persistence model so the
// pure function's tests build these by hand.
type PreflightStep struct {
	BodyText string
	BodyHTML string
}

// DomainAuthVerdict is one sending domain's last known SPF/DMARC
// authentication state, the slice of sendingdomain.Domain the domain_auth
// check needs.
type DomainAuthVerdict struct {
	// Checked is false when no check has ever completed for this domain (the
	// domain_auth check treats that the same as a failing one -- "warn with a
	// recheck remedy" -- rather than silently reporting it as passing).
	Checked    bool
	SPFFound   bool
	DMARCFound bool
}

// DomainAuthReader reads sending-domain SPF/DMARC verdicts for the workspace.
// Domain authentication is owned by the sendingdomain app domain; app/*
// packages never import each other (CLAUDE.md), so this narrow,
// consumer-defined interface is satisfied at wiring time in cmd/inroad by an
// adapter over sendingdomain.Service -- the same shape as Checker.
type DomainAuthReader interface {
	// DomainAuth returns the workspace's derived sending domains keyed by
	// lower-cased domain name (everything after the "@" in a mailbox's email).
	DomainAuth(ctx context.Context, ws uuid.UUID) (map[string]DomainAuthVerdict, error)
}

// PreflightInput is everything ComputePreflight needs, gathered by the
// service's thin loader (Service.Preflight) from the store, the campaign row,
// and the injected DomainAuthReader. ComputePreflight itself does no I/O, so
// unit tests build this by hand.
type PreflightInput struct {
	Steps           []PreflightStep
	Windows         []SendWindow
	Senders         []Sender
	AudienceCount   int64
	DomainAuth      map[string]DomainAuthVerdict
	TrackingEnabled bool
	// DailyLimit is the campaign-wide cap on sends per UTC day; nil means no
	// campaign-wide limit is set.
	DailyLimit *int
}

// ComputePreflight is the pure readiness computation behind GET
// /campaigns/{id}/preflight: no I/O, so every branch is exercised with
// hand-built PreflightInput values. Ready is true iff no check below is
// severity fail.
func ComputePreflight(in PreflightInput) PreflightReport {
	checks := []PreflightCheck{
		checkSequenceSteps(in),
		checkEmptyBodies(in),
		checkScheduleWindows(in),
		checkSenderPool(in),
		checkAudience(in),
		checkDomainAuth(in),
		checkTracking(in),
		checkDailyLimit(in),
		checkWarmupHealth(in),
	}
	ready := true
	for _, c := range checks {
		if c.Severity == SeverityFail {
			ready = false
		}
	}
	return PreflightReport{Ready: ready, Checks: checks}
}

// checkSequenceSteps fails a campaign with zero sequence steps: launching it
// would send nothing.
func checkSequenceSteps(in PreflightInput) PreflightCheck {
	if len(in.Steps) == 0 {
		return PreflightCheck{
			ID: CheckSequenceSteps, Severity: SeverityFail,
			Title:  "No sequence steps",
			Detail: "This campaign has no sequence steps, so launching it would send nothing.",
			Remedy: "Add at least one step to the sequence.",
		}
	}
	return PreflightCheck{
		ID: CheckSequenceSteps, Severity: SeverityPass,
		Title: "Sequence has steps", Detail: fmt.Sprintf("%d step(s) configured.", len(in.Steps)),
	}
}

// checkEmptyBodies warns when any step has neither a text nor an HTML body --
// still launchable, but that step would send an empty email.
func checkEmptyBodies(in PreflightInput) PreflightCheck {
	empty := 0
	for _, st := range in.Steps {
		if st.BodyText == "" && st.BodyHTML == "" {
			empty++
		}
	}
	if empty > 0 {
		return PreflightCheck{
			ID: CheckEmptyBodies, Severity: SeverityWarn,
			Title:  "Some steps have no body",
			Detail: fmt.Sprintf("%d step(s) have neither a text nor an HTML body.", empty),
			Remedy: "Add body content to every step before launching.",
		}
	}
	return PreflightCheck{
		ID: CheckEmptyBodies, Severity: SeverityPass,
		Title: "All steps have a body", Detail: "Every step has body content.",
	}
}

// checkScheduleWindows fails an empty week: no valid send instant exists.
func checkScheduleWindows(in PreflightInput) PreflightCheck {
	if len(in.Windows) == 0 {
		return PreflightCheck{
			ID: CheckScheduleWindows, Severity: SeverityFail,
			Title:  "No send window configured",
			Detail: "The campaign's weekly schedule has no open sending windows, so no valid send instant exists.",
			Remedy: "Configure at least one open window in the campaign's schedule.",
		}
	}
	return PreflightCheck{
		ID: CheckScheduleWindows, Severity: SeverityPass,
		Title: "Send window configured", Detail: fmt.Sprintf("%d window(s) open across the week.", len(in.Windows)),
	}
}

// checkSenderPool fails when no enabled sender in the pool has an active
// mailbox -- there is nothing to send from.
func checkSenderPool(in PreflightInput) PreflightCheck {
	eligible := 0
	for _, sd := range in.Senders {
		if sd.Enabled && sd.Status == mailboxStatusActive {
			eligible++
		}
	}
	if eligible == 0 {
		return PreflightCheck{
			ID: CheckSenderPool, Severity: SeverityFail,
			Title:  "No eligible sender",
			Detail: "No enabled sender in the pool has an active mailbox.",
			Remedy: "Enable at least one sender whose mailbox is active.",
		}
	}
	return PreflightCheck{
		ID: CheckSenderPool, Severity: SeverityPass,
		Title: "Sender pool ready", Detail: fmt.Sprintf("%d eligible sender(s).", eligible),
	}
}

// checkAudience fails a target list with zero unsuppressed contacts.
func checkAudience(in PreflightInput) PreflightCheck {
	if in.AudienceCount == 0 {
		return PreflightCheck{
			ID: CheckAudience, Severity: SeverityFail,
			Title:  "No audience",
			Detail: "The target list has 0 unsuppressed contacts.",
			Remedy: "Add contacts to the list, or review suppressions if every contact is opted out.",
		}
	}
	return PreflightCheck{
		ID: CheckAudience, Severity: SeverityPass,
		Title: "Audience ready", Detail: fmt.Sprintf("%d unsuppressed contact(s) in the list.", in.AudienceCount),
	}
}

// checkDomainAuth warns (never fails -- it is informational, invariant 39)
// when any sender domain is failing SPF or DMARC, or has never completed a
// check. When BOTH kinds of domain are present, the detail/remedy name both
// sets rather than only the failing one.
func checkDomainAuth(in PreflightInput) PreflightCheck {
	var failing, unchecked []string
	for _, d := range senderDomains(in.Senders) {
		v, ok := in.DomainAuth[d]
		switch {
		case !ok || !v.Checked:
			unchecked = append(unchecked, d)
		case !v.SPFFound || !v.DMARCFound:
			failing = append(failing, d)
		}
	}
	switch {
	case len(failing) > 0 && len(unchecked) > 0:
		return PreflightCheck{
			ID: CheckDomainAuth, Severity: SeverityWarn,
			Title: "Sending domain authentication needs attention",
			Detail: fmt.Sprintf("SPF or DMARC is missing for: %s. No completed check yet for: %s.",
				strings.Join(failing, ", "), strings.Join(unchecked, ", ")),
			Remedy: "Publish the missing SPF/DMARC records for the failing domain(s), and recheck domain authentication for the unchecked ones.",
		}
	case len(failing) > 0:
		return PreflightCheck{
			ID: CheckDomainAuth, Severity: SeverityWarn,
			Title:  "Sending domain authentication failing",
			Detail: fmt.Sprintf("SPF or DMARC is missing for: %s.", strings.Join(failing, ", ")),
			Remedy: "Publish the missing SPF/DMARC records for the affected domain(s).",
		}
	case len(unchecked) > 0:
		return PreflightCheck{
			ID: CheckDomainAuth, Severity: SeverityWarn,
			Title:  "Sending domain authentication not checked",
			Detail: fmt.Sprintf("No completed check yet for: %s.", strings.Join(unchecked, ", ")),
			Remedy: "Recheck domain authentication for the affected domain(s).",
		}
	default:
		return PreflightCheck{
			ID: CheckDomainAuth, Severity: SeverityPass,
			Title: "Sending domain authentication passing", Detail: "SPF and DMARC pass for every sender domain.",
		}
	}
}

// checkTracking is purely informational: tracking off never fails preflight.
func checkTracking(in PreflightInput) PreflightCheck {
	if !in.TrackingEnabled {
		return PreflightCheck{
			ID: CheckTracking, Severity: SeverityWarn,
			Title:  "Tracking disabled",
			Detail: "Open and click tracking are disabled for this campaign.",
			Remedy: "Enable tracking if you want open/click metrics (optional).",
		}
	}
	return PreflightCheck{
		ID: CheckTracking, Severity: SeverityPass,
		Title: "Tracking enabled", Detail: "Open and click tracking are enabled.",
	}
}

// checkDailyLimit warns when the campaign-wide daily limit is set HIGHER than
// the enabled pool's combined capacity today -- the limit can only ever lower
// throughput, so a limit above capacity is simply dead configuration, never a
// launch blocker.
func checkDailyLimit(in PreflightInput) PreflightCheck {
	if in.DailyLimit == nil {
		return PreflightCheck{
			ID: CheckDailyLimit, Severity: SeverityPass,
			Title: "No campaign-wide limit", Detail: "No campaign-wide daily limit is set; each sender's own cap applies.",
		}
	}
	sum := 0
	for _, sd := range in.Senders {
		if sd.Enabled {
			sum += sd.CapToday
		}
	}
	if *in.DailyLimit > sum {
		return PreflightCheck{
			ID: CheckDailyLimit, Severity: SeverityWarn,
			Title:  "Daily limit exceeds sender capacity",
			Detail: fmt.Sprintf("The campaign's daily limit (%d) is higher than the sender pool's combined capacity today (%d).", *in.DailyLimit, sum),
			Remedy: "Lower the daily limit, or add/enable more senders.",
		}
	}
	return PreflightCheck{
		ID: CheckDailyLimit, Severity: SeverityPass,
		Title:  "Daily limit within capacity",
		Detail: fmt.Sprintf("Daily limit %d is within the pool's combined capacity of %d today.", *in.DailyLimit, sum),
	}
}

// checkWarmupHealth warns when any pool mailbox is currently throttled or
// paused by the warmup engine -- the campaign can still launch, but will send
// slower than its configured caps promise.
func checkWarmupHealth(in PreflightInput) PreflightCheck {
	var affected []string
	for _, sd := range in.Senders {
		if sd.HealthState == nil {
			continue
		}
		switch *sd.HealthState {
		case sendcap.HealthThrottled, sendcap.HealthPaused:
			affected = append(affected, sd.Email)
		}
	}
	if len(affected) > 0 {
		return PreflightCheck{
			ID: CheckWarmupHealth, Severity: SeverityWarn,
			Title:  "Warmup health limiting some senders",
			Detail: fmt.Sprintf("Throttled or paused by warmup health: %s.", strings.Join(affected, ", ")),
			Remedy: "Wait for warmup health to recover, or adjust the sender pool.",
		}
	}
	return PreflightCheck{
		ID: CheckWarmupHealth, Severity: SeverityPass,
		Title: "Warmup health normal", Detail: "No pool mailbox is throttled or paused by warmup health.",
	}
}

// senderDomains returns the distinct, lower-cased domains the pool sends
// from, sorted for a deterministic detail message.
func senderDomains(senders []Sender) []string {
	seen := make(map[string]struct{}, len(senders))
	out := make([]string, 0, len(senders))
	for _, sd := range senders {
		d := domainOf(sd.Email)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// domainOf returns the lower-cased domain part of an email address, or "" if
// email carries no "@" (or nothing after it).
func domainOf(email string) string {
	i := strings.LastIndex(email, "@")
	if i < 0 || i == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[i+1:])
}

// Preflight computes GET /campaigns/{id}/preflight. It is the thin loader:
// every I/O call lives here, and the actual readiness judgement is delegated
// whole to the pure ComputePreflight so that logic is unit-tested without a
// database.
func (s *Service) Preflight(ctx context.Context, ws, campaignID uuid.UUID) (PreflightReport, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, ErrNotFound
	}
	steps, err := s.store.ListSteps(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, err
	}
	windows, err := s.store.ListWindows(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, err
	}
	senders, err := s.loadSenderPool(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, err
	}
	audience, err := s.store.CountUnsuppressedAudience(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, err
	}
	domainAuth, err := s.readDomainAuth(ctx, ws)
	if err != nil {
		return PreflightReport{}, err
	}
	return ComputePreflight(PreflightInput{
		Steps: toPreflightSteps(steps), Windows: windows, Senders: senders,
		AudienceCount: audience, DomainAuth: domainAuth,
		TrackingEnabled: c.TrackingEnabled, DailyLimit: dailyLimit(c.DailyLimit),
	}), nil
}

// readDomainAuth returns the workspace's domain-auth verdicts, or an empty
// map when no DomainAuthReader was wired (so the domain_auth check degrades to
// "unchecked" for every domain rather than the loader failing).
func (s *Service) readDomainAuth(ctx context.Context, ws uuid.UUID) (map[string]DomainAuthVerdict, error) {
	if s.domainAuth == nil {
		return map[string]DomainAuthVerdict{}, nil
	}
	return s.domainAuth.DomainAuth(ctx, ws)
}

// toPreflightSteps projects the persistence model onto the pure function's
// minimal input, decoupling ComputePreflight from gen.SequenceStep.
func toPreflightSteps(steps []gen.SequenceStep) []PreflightStep {
	out := make([]PreflightStep, len(steps))
	for i, st := range steps {
		out[i] = PreflightStep{BodyText: st.BodyText, BodyHTML: st.BodyHtml}
	}
	return out
}
