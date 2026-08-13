package campaign

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
	"github.com/inroad/inroad/internal/platform/warmup"
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
	CheckTokens          = "personalization_tokens"
	CheckVariantWeights  = "variant_weights"
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
// enough to detect an empty body and scan its personalization tokens,
// decoupled from the persistence model so the pure function's tests build
// these by hand.
type PreflightStep struct {
	// Subject is scanned for tokens but deliberately NOT counted by the
	// empty-body check: a follow-up step with no subject threads onto the
	// previous message (see replySubject), so an empty one is normal.
	Subject  string
	BodyText string
	BodyHTML string
	// Variants are the step's A/B alternatives. Every check that reads copy must
	// read these too: an alternative is a real email that real prospects receive,
	// so a token typo or an empty body in one is exactly as damaging as in the
	// base copy — and strictly harder to notice, because the step looks fine.
	Variants []PreflightVariant
	// BaseWeight is the step's own share of the A/B split; Variants carry theirs.
	// Zero everywhere means nothing can be selected and the step cannot send
	// (see checkVariantWeights).
	BaseWeight int
}

// PreflightVariant is one alternative's slice of a variant row.
type PreflightVariant struct {
	Label    string
	Weight   int
	Subject  string
	BodyText string
	BodyHTML string
}

// copies returns every candidate email this step can produce — the base copy
// first, then each variant — so a check can walk them uniformly instead of
// special-casing the base.
func (s PreflightStep) copies() []PreflightVariant {
	// With no alternatives, the base copy sends whatever its weight says —
	// exactly as the send path treats it (inprocess.selectVariant returns the
	// base immediately when there are no variant rows). BaseWeight only ever
	// describes a SPLIT, and there is nothing to split against, so a stray 0 here
	// must not read as "this step is retired".
	baseWeight := s.BaseWeight
	if len(s.Variants) == 0 {
		baseWeight = 1
	}
	out := make([]PreflightVariant, 0, len(s.Variants)+1)
	out = append(out, PreflightVariant{
		Label: "base", Weight: baseWeight,
		Subject: s.Subject, BodyText: s.BodyText, BodyHTML: s.BodyHTML,
	})
	return append(out, s.Variants...)
}

// eligibleCopies is the copies that can actually be selected.
func (s PreflightStep) eligibleCopies() []PreflightVariant {
	out := make([]PreflightVariant, 0, len(s.Variants)+1)
	for _, c := range s.copies() {
		if c.Weight > 0 {
			out = append(out, c)
		}
	}
	return out
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

// CustomFieldReader reads the workspace's live custom field keys. Custom fields
// are owned by the contact app domain and app/* packages never import each
// other (CLAUDE.md), so this consumer-defined interface is satisfied at wiring
// time in cmd/inroad by an adapter over contact.Service -- the same shape as
// DomainAuthReader and Checker.
type CustomFieldReader interface {
	// CustomFieldKeys returns only LIVE keys. An archived field's key must read
	// as unknown here: its values still exist on contacts, but no new contact
	// can be given one, so a token referring to it renders blank for everyone
	// imported since it was retired.
	CustomFieldKeys(ctx context.Context, ws uuid.UUID) ([]string, error)
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
	// CustomFieldKeys is the workspace's live custom field keys, against which
	// every {{custom.*}} token in the sequence is resolved.
	CustomFieldKeys []string
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
		checkTokens(in),
		checkVariantWeights(in),
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
	// Counts every candidate copy, not every step: a step whose base body is
	// filled in but whose variant B is blank still sends blank emails, to
	// whichever share of the audience the split sends B.
	//
	// A copy at weight 0 is skipped — it is retired and cannot be selected, so
	// warning about its body would be noise about an email nobody receives.
	empty := 0
	for _, st := range in.Steps {
		for _, c := range st.eligibleCopies() {
			if c.BodyText == "" && c.BodyHTML == "" {
				empty++
			}
		}
	}
	if empty > 0 {
		return PreflightCheck{
			ID: CheckEmptyBodies, Severity: SeverityWarn,
			Title:  "Some steps have no body",
			Detail: fmt.Sprintf("%d step(s) or variant(s) have neither a text nor an HTML body.", empty),
			Remedy: "Add body content to every step and variant before launching.",
		}
	}
	return PreflightCheck{
		ID: CheckEmptyBodies, Severity: SeverityPass,
		Title: "All steps have a body", Detail: "Every step and variant has body content.",
	}
}

// checkVariantWeights FAILS a step whose base copy and every variant are all at
// weight 0.
//
// Nothing can be selected in that state, so every send for that step errors at
// the send path's backstop — one enrollment at a time, after launch, with a
// message about weights that nobody is watching for. The variant API already
// refuses the edit that would produce it (sequencestep.ErrNoEligibleVariant);
// this catches the campaign that was edited into it by some other route, and
// says so before anyone launches.
func checkVariantWeights(in PreflightInput) PreflightCheck {
	var stalled []string
	for _, st := range in.Steps {
		if len(st.eligibleCopies()) == 0 {
			stalled = append(stalled, fmt.Sprintf("step %d", len(stalled)+1))
		}
	}
	if len(stalled) > 0 {
		return PreflightCheck{
			ID: CheckVariantWeights, Severity: SeverityFail,
			Title:  "A step has no sendable variant",
			Detail: fmt.Sprintf("%d step(s) have every variant at weight 0, so they cannot send anything.", len(stalled)),
			Remedy: "Give the step's own copy or one of its variants a weight above zero.",
		}
	}
	return PreflightCheck{
		ID: CheckVariantWeights, Severity: SeverityPass,
		Title: "Every step can send", Detail: "Each step has at least one variant with a weight above zero.",
	}
}

// checkTokens FAILS a sequence containing a personalization token nothing will
// substitute.
//
// Fail rather than warn, which is a deliberately harsher verdict than the
// neighbouring content checks: an empty body sends a blank email the operator
// can see is blank the moment they look, whereas a bad token produces an email
// that looks fine in the editor and arrives reading "Hi {{firstname}}" or
// "Hi ,". There is also no legitimate reason to launch with one -- a token
// nothing resolves was a typo or a field that has since been archived, never an
// intent -- so there is nothing to weigh against blocking.
func checkTokens(in PreflightInput) PreflightCheck {
	templates := make([]string, 0, len(in.Steps)*3)
	for _, st := range in.Steps {
		for _, c := range st.copies() {
			templates = append(templates, c.Subject, c.BodyText, c.BodyHTML)
		}
	}
	unknown := UnknownTokens(in.CustomFieldKeys, templates...)
	if len(unknown) > 0 {
		return PreflightCheck{
			ID: CheckTokens, Severity: SeverityFail,
			Title:  "Unknown personalization tokens",
			Detail: fmt.Sprintf("%d placeholder(s) resolve to nothing: %s.", len(unknown), strings.Join(unknown, ", ")),
			Remedy: "Fix the spelling, or define the custom field these tokens refer to under Settings → Custom fields.",
		}
	}
	return PreflightCheck{
		ID: CheckTokens, Severity: SeverityPass,
		Title: "Personalization tokens resolve", Detail: "Every placeholder maps to a contact field.",
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

// checkWarmupHealth reports on both warmup axes, which bite differently.
//
// A pool LANE that may not take new leads (quarantine, blocked, pending_auth) is a
// FAIL: those mailboxes are withheld from the pool entirely, so launching would
// either send nothing or silently concentrate the whole campaign on the remaining
// senders. This is the first case where this check can stop a launch, so the
// message names the lane and what clears it rather than reporting a bare score.
//
// Reduced capacity — a degraded health state, or a low-volume evidence-gathering
// lane — stays a WARNING: the campaign can launch, it will just send slower than
// its configured caps promise.
func checkWarmupHealth(in PreflightInput) PreflightCheck {
	var withheld, unauthenticated, reduced []string
	for _, sd := range in.Senders {
		// The same predicate the senders panel and the rotation use, so the warning
		// here and the block there cannot disagree. It answers for the mailbox AND
		// its organizational domain: a quarantined mailbox has almost certainly
		// damaged the standing of every sibling sending from the same domain.
		if warmup.NewLeadsWithheld(deref(sd.Lane), deref(sd.DomainLane)) {
			withheld = append(withheld, withheldSender(sd))
			continue
		}
		// pending_auth is driven by sending_domains, which security.md invariant 39
		// scopes as ADVISORY: "an advisory that turns out to be wrong must not be able
		// to stop a campaign". A spoofed or merely un-swept DNS answer would otherwise
		// block a launch, and a fresh install has no sending_domains row at all. It
		// still WARNS, and warmup itself still refuses to send unauthenticated mail
		// (LaneMaySend is false) — the advisory just cannot veto a campaign. It is
		// checked AFTER the withheld test so an unauthenticated mailbox that is also
		// quarantined reports the containment, which is the one an operator can act on.
		if sd.Lane != nil && *sd.Lane == warmup.LanePendingAuth {
			unauthenticated = append(unauthenticated, sd.Email)
			continue
		}
		degraded := false
		if sd.HealthState != nil {
			switch *sd.HealthState {
			case sendcap.HealthUnknown, sendcap.HealthWatch, sendcap.HealthThrottled, sendcap.HealthPaused:
				degraded = true
			}
		}
		gathering := sd.Lane != nil && (*sd.Lane == warmup.LaneProbation || *sd.Lane == warmup.LaneRecovery)
		if degraded || gathering {
			reduced = append(reduced, sd.Email)
		}
	}
	if len(withheld) > 0 {
		return PreflightCheck{
			ID: CheckWarmupHealth, Severity: SeverityFail,
			Title:  "Senders withheld from the warmup pool",
			Detail: fmt.Sprintf("These mailboxes cannot take new leads: %s.", strings.Join(withheld, ", ")),
			Remedy: "A withheld mailbox rejoins the pool by passing domain authentication " +
				"and then earning a clean evidence window; a blocked one needs operator approval. " +
				"A mailbox withheld by its domain waits for the sibling that is containing it. " +
				"Remove them from the pool to launch now. Replies to existing conversations are unaffected.",
		}
	}
	if len(unauthenticated) > 0 {
		return PreflightCheck{
			ID: CheckWarmupHealth, Severity: SeverityWarn,
			Title:  "Domain authentication has not passed for some senders",
			Detail: fmt.Sprintf("SPF/DMARC has not been confirmed for: %s.", strings.Join(unauthenticated, ", ")),
			Remedy: "Publish SPF and DMARC for these domains. Warmup is paused for them until the " +
				"next DNS check passes; campaigns can still launch.",
		}
	}
	if len(reduced) > 0 {
		return PreflightCheck{
			ID: CheckWarmupHealth, Severity: SeverityWarn,
			Title:  "Warmup evidence or health limiting some senders",
			Detail: fmt.Sprintf("Warmup has reduced capacity for: %s.", strings.Join(reduced, ", ")),
			Remedy: "Wait for warmup health to recover, or adjust the sender pool.",
		}
	}
	return PreflightCheck{
		ID: CheckWarmupHealth, Severity: SeverityPass,
		Title: "Warmup health normal", Detail: "No pool mailbox is withheld or capacity-limited by warmup.",
	}
}

// withheldSender names a refused mailbox AND why, distinguishing its own lane
// from a sibling's. The two have different remedies — one waits for its own
// evidence, the other for another mailbox's — so a message that said only
// "withheld" would send the operator to look at the wrong mailbox.
func withheldSender(sd Sender) string {
	if sd.Lane != nil && !warmup.LaneMayTakeNewLead(*sd.Lane) {
		return fmt.Sprintf("%s (%s)", sd.Email, *sd.Lane)
	}
	return fmt.Sprintf("%s (domain %s is %s)", sd.Email, domainOf(sd.Email), deref(sd.DomainLane))
}

// deref reads an optional lane as the empty string, which every lane predicate
// already treats as "not warming up, therefore not gated".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	customKeys, err := s.readCustomFieldKeys(ctx, ws)
	if err != nil {
		return PreflightReport{}, err
	}
	variants, err := s.store.ListStepVariants(ctx, ws, campaignID)
	if err != nil {
		return PreflightReport{}, err
	}
	return ComputePreflight(PreflightInput{
		Steps: toPreflightSteps(steps, variants), Windows: windows, Senders: senders,
		AudienceCount: audience, DomainAuth: domainAuth, CustomFieldKeys: customKeys,
		TrackingEnabled: c.TrackingEnabled, DailyLimit: optionalInt(c.DailyLimit),
	}), nil
}

// readCustomFieldKeys returns the workspace's live custom field keys.
//
// Unlike readDomainAuth, an unwired reader is NOT degraded to an empty result
// here -- it is an error. An empty key set makes every {{custom.*}} token in
// the sequence look unknown, so degrading would turn a wiring mistake into a
// campaign that cannot be launched, with a message blaming the operator's
// templates. Failing the preflight request says the truth: this check could not
// run.
func (s *Service) readCustomFieldKeys(ctx context.Context, ws uuid.UUID) ([]string, error) {
	if s.customFields == nil {
		return nil, errors.New("preflight: no custom field reader wired")
	}
	return s.customFields.CustomFieldKeys(ctx, ws)
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
func toPreflightSteps(steps []gen.SequenceStep, variants map[uuid.UUID][]PreflightVariant) []PreflightStep {
	out := make([]PreflightStep, len(steps))
	for i, st := range steps {
		out[i] = PreflightStep{
			Subject: st.Subject, BodyText: st.BodyText, BodyHTML: st.BodyHtml,
			BaseWeight: int(st.VariantWeight), Variants: variants[st.ID],
		}
	}
	return out
}
