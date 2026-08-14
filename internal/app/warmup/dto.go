package warmup

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Participant is the domain view of a warmup_participants row. It mirrors the
// persistence columns 1:1 (there is no secret to strip here, unlike mailbox),
// but exists so the domain surface returns a package-owned type instead of the
// generated gen.WarmupParticipant — the decoupling seam the service depends on.
type Participant struct {
	MailboxID     uuid.UUID
	WorkspaceID   uuid.UUID
	Enabled       bool
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
	StartedAt     pgtype.Timestamptz
	HealthState   string
	HealthReason  string
	PausedUntil   pgtype.Timestamptz
	CreatedAt     pgtype.Timestamptz
	UpdatedAt     pgtype.Timestamptz
}

func participantFromGen(p gen.WarmupParticipant) Participant {
	return Participant{
		MailboxID:     p.MailboxID,
		WorkspaceID:   p.WorkspaceID,
		Enabled:       p.Enabled,
		StartVolume:   p.StartVolume,
		MaxVolume:     p.MaxVolume,
		RampIncrement: p.RampIncrement,
		ReplyRate:     p.ReplyRate,
		StartedAt:     p.StartedAt,
		HealthState:   p.HealthState,
		HealthReason:  p.HealthReason,
		PausedUntil:   p.PausedUntil,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// DayStat is one day's warmup counters for a mailbox, used by the detail series.
type DayStat struct {
	Day      pgtype.Date
	Sent     int32
	Received int32
	Inbox    int32
	Spam     int32
	Replies  int32
}

func dayStatFromGen(s gen.WarmupDailyStat) DayStat {
	return DayStat{
		Day:      s.Day,
		Sent:     s.Sent,
		Received: s.Received,
		Inbox:    s.Inbox,
		Spam:     s.Spam,
		Replies:  s.Replies,
	}
}

// RouteRow is one (destination ESP, trailing-7-day counters) cell of a mailbox's
// route matrix: how its warmup mail placed at ONE destination.
//
// The counters are exactly the four the overview rollup produces, and for the same
// reasons — Inbox7d INCLUDES the tabbed landings (a tab is a sub-location inside
// the inbox), and TabCapable7d is the tabbed rate's own denominator. The
// difference is only the grouping, so a route's numbers are read the same way a
// mailbox's pooled numbers are, one destination at a time.
//
// The rates are NOT computed here: the service turns these into the wire DTO,
// where the sample floor is applied. Keeping the store's view as counts is what
// makes the per-route denominator visible at the boundary instead of implied.
type RouteRow struct {
	// DestinationESP is esp.ESP's vocabulary: google | microsoft | other | unknown.
	// `unknown` means the destination was never resolved (no MX cache entry when the
	// message arrived, or an observation older than routes); `other` means it WAS
	// resolved and is neither Google nor Microsoft. Collapsing them would tell an
	// operator they measured a route nobody looked at.
	DestinationESP string
	Inbox7d        int64
	Spam7d         int64
	Tabbed7d       int64
	TabCapable7d   int64
}

func routeRowFromGen(r gen.ListWarmupRoutesRow) RouteRow {
	return RouteRow{
		DestinationESP: r.DestinationEsp,
		Inbox7d:        r.Inbox7d,
		Spam7d:         r.Spam7d,
		Tabbed7d:       r.Tabbed7d,
		TabCapable7d:   r.TabCapable7d,
	}
}

// UpsertParams carries the ramp settings for enabling or updating a
// participant. It is a domain-owned struct (not the generated params type) so
// the Store interface stays free of gen references, keeping the seam minimal.
type UpsertParams struct {
	MailboxID     uuid.UUID
	WorkspaceID   uuid.UUID
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
}

// OverviewRow is one participant enriched for the workspace overview: its ramp
// and health fields, the mailbox email, the trailing-7-day SENDER placement sums
// (inbox/spam), and today's sent count — all resolved in one workspace-pinned
// read. Inbox7d/Spam7d are the numerator/denominator inputs the service turns
// into inbox_rate_7d / spam_rate_7d; the denominator is Inbox7d+Spam7d (observed
// placements of this mailbox's SENT warmup mail), not received volume.
//
// Tabbed7d/TabCapable7d are the tabbed rate and ITS OWN denominator. Inbox7d
// already INCLUDES the tabbed landings (a tabbed message landed in the inbox), so
// the two pairs are not disjoint by design: the tab is a sub-location within the
// inbox, and reporting it must not move inbox_rate_7d.
type OverviewRow struct {
	MailboxID     uuid.UUID
	Enabled       bool
	StartVolume   int32
	MaxVolume     int32
	RampIncrement int32
	ReplyRate     float32
	StartedAt     pgtype.Timestamptz
	HealthState   string
	HealthReason  string
	Lane          string
	LaneReason    string
	Email         string
	Inbox7d       int64
	Spam7d        int64
	// Tabbed7d counts placements a provider positively identified as a tab.
	// TabCapable7d counts the inbox-side placements whose READER could have
	// identified one — the only honest denominator for a rate over a signal that is
	// undetectable on an entire provider class.
	Tabbed7d     int64
	TabCapable7d int64
	// The LATEST identity this mailbox's warmup mail was observed sending under and
	// the verdicts its receivers reached on it (design §4). Attributed to the SENDER
	// exactly as the placement counters above are, because it IS the same
	// observation row.
	//
	// IdentityObservedAt is the PRESENCE signal, and the only one: the five values
	// beside it are COALESCEd to their column defaults by the query, so a mailbox
	// with no identity facts is indistinguishable from a genuinely unsigned one
	// except by the missing timestamp. A reader that tests any other field is
	// reading a default as an observation.
	//
	// Not windowed to 7 days like the rates above: an identity is a state, not a
	// rate, and the last one seen remains true until a newer one contradicts it.
	// observed_at travels to the client so it can judge the staleness itself.
	IdentityDKIMDomain       string
	IdentityReturnPathDomain string
	IdentitySPFResult        string
	IdentityDKIMResult       string
	IdentityDMARCResult      string
	IdentityObservedAt       pgtype.Timestamptz
	TodaySent                int32
}

func overviewRowFromGen(r gen.ListWarmupOverviewRowsRow) OverviewRow {
	return OverviewRow{
		MailboxID:     r.MailboxID,
		Enabled:       r.Enabled,
		StartVolume:   r.StartVolume,
		MaxVolume:     r.MaxVolume,
		RampIncrement: r.RampIncrement,
		ReplyRate:     r.ReplyRate,
		StartedAt:     r.StartedAt,
		HealthState:   r.HealthState,
		HealthReason:  r.HealthReason,
		Lane:          r.Lane,
		LaneReason:    r.LaneReason,
		Email:         r.Email,
		Inbox7d:       r.Inbox7d,
		Spam7d:        r.Spam7d,
		Tabbed7d:      r.Tabbed7d,
		TabCapable7d:  r.TabCapable7d,

		IdentityDKIMDomain:       r.IdentityDkimDomain,
		IdentityReturnPathDomain: r.IdentityReturnPathDomain,
		IdentitySPFResult:        r.IdentitySpfResult,
		IdentityDKIMResult:       r.IdentityDkimResult,
		IdentityDMARCResult:      r.IdentityDmarcResult,
		IdentityObservedAt:       r.IdentityObservedAt,
		TodaySent:                r.TodaySent,
	}
}

// Transition is one persisted row of the append-only decision record
// (warmup_state_transitions). The lane fields are pointers because rows written
// before pool lanes existed genuinely had no lane: the migration deliberately
// left them NULL rather than fabricating 'probation' in an audit trail.
// BouncePopulation is a pointer for the same reason — a row written before the
// campaign/warmup bounce split does not know which arm its samples counted.
type Transition struct {
	ID               uuid.UUID
	CreatedAt        pgtype.Timestamptz
	FromState        string
	ToState          string
	ReasonCode       string
	Reason           string
	FromLane         *string
	ToLane           *string
	LaneReasonCode   *string
	LaneReason       *string
	PlacementSamples int32
	SpamRate         float32
	BouncePopulation *string
	BounceSamples    int32
	BounceRate       float32
	ComplaintSamples int32
	ComplaintRate    float32
	InvalidTokens    int32
	PolicyVersion    string
}

func transitionFromGen(r gen.ListWarmupTransitionsRow) Transition {
	return Transition{
		ID:               r.ID,
		CreatedAt:        r.CreatedAt,
		FromState:        r.FromState,
		ToState:          r.ToState,
		ReasonCode:       r.ReasonCode,
		Reason:           r.Reason,
		FromLane:         r.FromLane,
		ToLane:           r.ToLane,
		LaneReasonCode:   r.LaneReasonCode,
		LaneReason:       r.LaneReason,
		PlacementSamples: r.PlacementSamples,
		SpamRate:         r.SpamRate,
		BouncePopulation: r.BouncePopulation,
		BounceSamples:    r.BounceSamples,
		BounceRate:       r.BounceRate,
		ComplaintSamples: r.ComplaintSamples,
		ComplaintRate:    r.ComplaintRate,
		InvalidTokens:    r.InvalidTokens,
		PolicyVersion:    r.PolicyVersion,
	}
}

// ---------------------------------------------------------------------------
// API response DTOs (spec §10 / api/openapi.yaml). These carry the snake_case
// JSON contract the frontend consumes. They are DISTINCT from the persistence
// view types above (Participant/DayStat/OverviewRow): the service maps domain
// types into these so the wire shape and the storage shape stay decoupled.
// ---------------------------------------------------------------------------

// WarmupParticipantDTO is the WarmupParticipant schema: a mailbox's enrollment
// state plus its computed today_sent / today_target.
type WarmupParticipantDTO struct {
	MailboxID     string  `json:"mailbox_id"`
	Enabled       bool    `json:"enabled"`
	StartVolume   int32   `json:"start_volume"`
	MaxVolume     int32   `json:"max_volume"`
	RampIncrement int32   `json:"ramp_increment"`
	ReplyRate     float32 `json:"reply_rate"`
	HealthState   string  `json:"health_state"`
	HealthReason  string  `json:"health_reason"`
	StartedAt     string  `json:"started_at"`
	TodaySent     int32   `json:"today_sent"`
	TodayTarget   int32   `json:"today_target"`
}

// WarmupMailboxDTO is the WarmupMailbox schema: a participant enriched with the
// mailbox email and rolling 7-day placement rates for the overview.
type WarmupMailboxDTO struct {
	MailboxID    string `json:"mailbox_id"`
	Email        string `json:"email"`
	Enabled      bool   `json:"enabled"`
	HealthState  string `json:"health_state"`
	HealthReason string `json:"health_reason"`
	// The POOL ELIGIBILITY axis. Required by the schema since lanes shipped, but
	// unpopulated until now, so the SPA saw undefined and rendered every mailbox as
	// "probation" regardless of its real lane.
	Lane              string `json:"lane"`
	LaneReason        string `json:"lane_reason"`
	TodaySent         int32  `json:"today_sent"`
	TodayTarget       int32  `json:"today_target"`
	PlacementSample7d int64  `json:"placement_sample_7d"`
	// TabbedRate7d is the fraction of this mailbox's categorisable warmup mail that
	// landed in a TAB rather than the primary inbox. NULL — never 0.0 — when nothing
	// that OBSERVED this mailbox's mail could report a category. A zero would read as
	// a confident clean rate for a mailbox whose tabs are merely invisible, i.e. for
	// most of a self-hosted pool.
	//
	// Note WHOSE capability decides it. Placement is attributed to the SENDER but
	// recorded by the RECIPIENT's poller (Inbox7d works the same way), so the
	// capability that fills this denominator belongs to the PARTNERS: a Gmail sender
	// whose warmup peers are all IMAP reports null even though its own provider
	// categorises perfectly well. The UI must therefore say "no partner could report
	// a tab" and never make a statement about this mailbox's own provider — that
	// would point an operator at a mailbox that is working fine.
	//
	// Reported for visibility only: no threshold, lane or promotion decision reads
	// it. A signal invisible on a whole provider class must not gate anything, or
	// promotion becomes unreachable for every SMTP mailbox.
	TabbedRate7d *float64 `json:"tabbed_rate_7d"`
	// TabCapableSample7d is TabbedRate7d's denominator, counted separately because
	// pooling observations that structurally cannot report a tab would dilute the
	// rate toward zero and make an untested pool read clean.
	TabCapableSample7d int64    `json:"tab_capable_sample_7d"`
	InboxRate7d        *float64 `json:"inbox_rate_7d"`
	SpamRate7d         *float64 `json:"spam_rate_7d"`
	// Identity is NULL — not a block of empty defaults — when no observation of this
	// mailbox has carried identity facts. The two are different facts and a client
	// must be able to tell them apart: an unsigned sender that a receiver reported on
	// is a finding, while a mailbox nobody has observed yet is an absence.
	//
	// Reported for visibility only, like the tabbed rate above: no threshold, lane or
	// promotion decision reads any of it (design §7).
	Identity *WarmupIdentityDTO `json:"identity"`
}

// WarmupIdentityDTO is the WarmupMailbox.identity schema: the latest observed
// SENDING identity of a mailbox's warmup mail, and the verdicts the RECEIVING
// providers reached on it.
//
// The domains are "" when absent or unparseable, because absent and unparseable
// are the same fact to a reader and one representation avoids a three-way
// condition at every use (design §5).
//
// `unknown` and `none` are NOT the same verdict and a UI that renders them alike
// is wrong. `unknown` means no Authentication-Results header could be trusted to
// speak for the receiving system (RFC 8601 §5) — nobody reported anything, which
// is permanent for a provider that stamps nothing and is never a failure. `none`
// is the receiver's actual finding: it checked, and there was no SPF record, no
// signature, or no DMARC policy to check against.
type WarmupIdentityDTO struct {
	DKIMDomain       string `json:"dkim_domain"`
	ReturnPathDomain string `json:"return_path_domain"`
	SPFResult        string `json:"spf_result"`
	DKIMResult       string `json:"dkim_result"`
	DMARCResult      string `json:"dmarc_result"`
	// ObservedAt is RFC3339 UTC. It ships with the verdicts because they are not
	// windowed: an identity observed months ago is still reported, and only this
	// tells the client how much to trust it.
	ObservedAt string `json:"observed_at"`
}

// WarmupOverviewDTO is the WarmupOverview schema: the pool summary plus per
// mailbox health/placement. active is true when pool_size >= 2.
type WarmupOverviewDTO struct {
	PoolSize  int                `json:"pool_size"`
	Active    bool               `json:"active"`
	Mailboxes []WarmupMailboxDTO `json:"mailboxes"`
}

// WarmupDayStatDTO is the WarmupDayStat schema: one UTC day of counters.
type WarmupDayStatDTO struct {
	Day      string `json:"day"`
	Sent     int32  `json:"sent"`
	Received int32  `json:"received"`
	Inbox    int32  `json:"inbox"`
	Spam     int32  `json:"spam"`
	Replies  int32  `json:"replies"`
}

// WarmupRouteDTO is one row of the WarmupDetail route matrix: how this mailbox's
// warmup mail placed at ONE destination over the trailing 7 days.
//
// It is on the DETAIL endpoint and not the overview deliberately — a matrix on
// every row of the pool list would bloat it — and it is the single most actionable
// thing the engine can tell an operator: "your mail to Microsoft is going to spam"
// has an obvious next step where "your spam rate is 14%" does not.
//
// Reported for visibility only. No threshold, lane or promotion decision reads a
// per-route rate, and the reason is NOT the one the tabbed rate and the auth
// verdicts give. Those are structurally unobservable on a whole provider class, so
// gating them would penalise that class forever. A per-route rate is fully
// observable wherever the route exists; what is missing is CALIBRATION — nobody has
// yet seen what a normal Google→Microsoft warmup spam rate looks like in this
// system, so a threshold set today would be a guess dressed as a policy. That
// condition expires once real matrices exist, which is exactly why this ships
// before the slice that would consume it (design §7).
type WarmupRouteDTO struct {
	// DestinationESP is google | microsoft | other | unknown — where the mail was
	// DELIVERED, decided by the recipient domain's MX and recorded on each
	// observation when it arrived, never re-derived from a mailbox's current row.
	DestinationESP string `json:"destination_esp"`
	// PlacementSample7d is THIS route's own denominator: inbox-side plus spam
	// placements on this route alone, never the mailbox's pooled total. Splitting a
	// window by destination shrinks every cell — a four-route pool quarters every
	// denominator — which is why the sample travels with the rates rather than being
	// left for the reader to assume.
	PlacementSample7d int64 `json:"placement_sample_7d"`
	// The three rates are NULL — never 0.0 — when this route has fewer than
	// MinPlacementSamples placements. An unproven route is not a clean one, and a
	// zero would read as a measurement on a cell that has barely been sampled.
	InboxRate7d *float64 `json:"inbox_rate_7d"`
	SpamRate7d  *float64 `json:"spam_rate_7d"`
	// TabbedRate7d keeps its OWN denominator inside the route, exactly as it does on
	// the overview: the categorisable landings, not the route's placements. It is
	// additionally null when nothing that observed this route could report a
	// category at all.
	TabbedRate7d       *float64 `json:"tabbed_rate_7d"`
	TabCapableSample7d int64    `json:"tab_capable_sample_7d"`
}

// WarmupDetailDTO is the WarmupDetail schema: one participant, its daily series
// (oldest first, up to 30 days), and its destination-route matrix.
//
// Routes is never null — `[]` when nothing was observed — and is ordered by
// destination_esp so the UI and the tests are stable.
//
// A pool whose mailboxes are all on one ESP has exactly ONE route, and that is not
// "clean across the board": it is one cell, and it says nothing whatsoever about
// how this mailbox's mail performs anywhere else. Warmup partners are the
// workspace's own mailboxes, so the routes measurable here are exactly the ESPs
// present in that pool and no others (design §3). A client rendering this must say
// "one route in this pool" rather than presenting a single clean row as a matrix.
type WarmupDetailDTO struct {
	Participant WarmupParticipantDTO `json:"participant"`
	Series      []WarmupDayStatDTO   `json:"series"`
	Routes      []WarmupRouteDTO     `json:"routes"`
}

// WarmupTransitionDTO is the WarmupTransition schema: one automated state change
// and the evidence behind it.
//
// The four lane fields are nullable in the contract and stay pointers here, so a
// pre-lane row serializes as null rather than as an empty string a UI would have
// to guess about. The rates are CONFIDENCE-ADJUSTED lower bounds, not observed
// fractions — the schema says so, because rendering them as raw percentages would
// misreport every thin sample.
//
// bounce_population is nullable on the same grounds and matters for the same
// reason: bounce_samples/bounce_rate describe ONE population (campaign or warmup
// hard bounces, never both — pooling them is the dilution defect Phase 1 removed),
// so a client rendering the pair without the label would report a campaign figure
// as a warmup one.
type WarmupTransitionDTO struct {
	ID               string  `json:"id"`
	CreatedAt        string  `json:"created_at"`
	FromState        string  `json:"from_state"`
	ToState          string  `json:"to_state"`
	ReasonCode       string  `json:"reason_code"`
	Reason           string  `json:"reason"`
	FromLane         *string `json:"from_lane"`
	ToLane           *string `json:"to_lane"`
	LaneReasonCode   *string `json:"lane_reason_code"`
	LaneReason       *string `json:"lane_reason"`
	PlacementSamples int32   `json:"placement_samples"`
	SpamRate         float64 `json:"spam_rate"`
	BouncePopulation *string `json:"bounce_population"`
	BounceSamples    int32   `json:"bounce_samples"`
	BounceRate       float64 `json:"bounce_rate"`
	ComplaintSamples int32   `json:"complaint_samples"`
	ComplaintRate    float64 `json:"complaint_rate"`
	InvalidTokens    int32   `json:"invalid_tokens"`
	PolicyVersion    string  `json:"policy_version"`
}

// WarmupTransitionPageDTO is the WarmupTransitionPage schema. transitions is
// never null: an empty history is [], because a client distinguishing "no rows"
// from "field missing" is a distinction with no meaning here.
type WarmupTransitionPageDTO struct {
	Transitions []WarmupTransitionDTO `json:"transitions"`
}
