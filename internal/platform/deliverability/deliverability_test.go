package deliverability

import (
	"fmt"
	"math"
	"testing"
)

func ptr(n int) *int { return &n }

// component finds one component by key. Every Score carries all five, so a
// missing key is a bug in Compute rather than a test condition.
func component(t *testing.T, s Score, key string) Component {
	t.Helper()
	for _, c := range s.Components {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("score has no %q component", key)
	return Component{}
}

func TestRateIsGuardedAndPercent(t *testing.T) {
	cases := []struct {
		part, whole int
		want        float64
	}{
		{0, 0, 0},   // no sample at all
		{5, 0, 0},   // events with no sample: not an infinite rate
		{1, -3, 0},  // a negative whole cannot happen, and must not panic
		{-1, 10, 0}, // nor a negative part
		{1, 100, 1},
		{1, 8, 12.5},
		{1, 3, 33.333333},
		{50, 50, 100},
	}
	for _, c := range cases {
		// Compared with a tolerance: 1/3 has no exact float64 representation, and
		// the assertion is about the arithmetic being right, not about which of two
		// equally valid roundings the compiler picked.
		if got := Rate(c.part, c.whole); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("Rate(%d, %d) = %v, want %v", c.part, c.whole, got, c.want)
		}
	}
}

// The breaker's whole safety case: below MinDelivered it cannot fire, at ANY
// ratio. Table-driven from 1..49 delivered with a 100% bounce rate — the worst
// possible evidence on the smallest possible sample.
func TestBreakerNeverFiresBelowMinimumSample(t *testing.T) {
	for delivered := 1; delivered < MinDelivered; delivered++ {
		in := Inputs{Delivered: delivered, Bounced: delivered, Complained: ptr(delivered)}
		v := Breach(in, DefaultThresholds())
		if v.State != VerdictOk {
			t.Errorf("delivered=%d bounced=%d (100%%): got %q, want ok — invariant 1 broken",
				delivered, delivered, v.State)
		}
		if v.Reason != "" {
			t.Errorf("delivered=%d: verdict carries reason %q below the sample floor", delivered, v.Reason)
		}
	}
}

// The floor is a floor, not a mute: at exactly MinDelivered the same ratio pauses.
func TestBreakerFiresAtExactlyMinimumSample(t *testing.T) {
	in := Inputs{Delivered: MinDelivered, Bounced: MinDelivered}
	v := Breach(in, DefaultThresholds())
	if v.State != VerdictPause {
		t.Fatalf("delivered=%d at 100%% bounce: got %q, want pause", MinDelivered, v.State)
	}
	if v.Reason != ReasonBounceSpike || v.Metric != MetricBounceRate {
		t.Errorf("reason/metric = %q/%q, want %q/%q", v.Reason, v.Metric, ReasonBounceSpike, MetricBounceRate)
	}
	if v.Value != 100 || v.Threshold != DefaultBouncePausePct || v.Delivered != MinDelivered {
		t.Errorf("verdict = %+v, want value 100 threshold %v delivered %d",
			v, DefaultBouncePausePct, MinDelivered)
	}
}

func TestBreachVerdicts(t *testing.T) {
	cases := []struct {
		name       string
		in         Inputs
		wantState  VerdictState
		wantReason string
	}{
		{
			name:      "clean at a large sample",
			in:        Inputs{Delivered: 1000, Bounced: 5},
			wantState: VerdictOk,
		},
		{
			// 3.9% is below half of 8%, so not even a warning.
			name:      "just under the warn band",
			in:        Inputs{Delivered: 1000, Bounced: 39},
			wantState: VerdictOk,
		},
		{
			name:       "at the warn band",
			in:         Inputs{Delivered: 1000, Bounced: 40},
			wantState:  VerdictWarn,
			wantReason: ReasonBounceSpike,
		},
		{
			name:       "just under the pause threshold still only warns",
			in:         Inputs{Delivered: 1000, Bounced: 79},
			wantState:  VerdictWarn,
			wantReason: ReasonBounceSpike,
		},
		{
			name:       "at the pause threshold",
			in:         Inputs{Delivered: 1000, Bounced: 80},
			wantState:  VerdictPause,
			wantReason: ReasonBounceSpike,
		},
		{
			name:       "complaint spike on a clean bounce rate",
			in:         Inputs{Delivered: 1000, Bounced: 0, Complained: ptr(15)},
			wantState:  VerdictPause,
			wantReason: ReasonComplaintSpike,
		},
		{
			name:       "complaint warn band",
			in:         Inputs{Delivered: 1000, Bounced: 0, Complained: ptr(8)},
			wantState:  VerdictWarn,
			wantReason: ReasonComplaintSpike,
		},
		{
			// An unmeasured complaint count can never breach: nothing was observed.
			name:      "nil complaints cannot breach",
			in:        Inputs{Delivered: 1000, Bounced: 0, Complained: nil},
			wantState: VerdictOk,
		},
		{
			// Pause beats warn: bounce pauses, so the softer complaint warn is not
			// what gets reported.
			name:       "pause wins over warn",
			in:         Inputs{Delivered: 1000, Bounced: 90, Complained: ptr(8)},
			wantState:  VerdictPause,
			wantReason: ReasonBounceSpike,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Breach(c.in, DefaultThresholds())
			if v.State != c.wantState {
				t.Fatalf("state = %q, want %q (verdict %+v)", v.State, c.wantState, v)
			}
			if v.Reason != c.wantReason {
				t.Errorf("reason = %q, want %q", v.Reason, c.wantReason)
			}
		})
	}
}

// The warn band must fire strictly below pause across the whole range, and never
// at or above it — the property, not one sampled point.
func TestWarnBandSitsStrictlyBelowPause(t *testing.T) {
	th := DefaultThresholds()
	for bounced := 0; bounced <= 200; bounced++ {
		in := Inputs{Delivered: 1000, Bounced: bounced}
		v := Breach(in, th)
		rate := Rate(bounced, 1000)
		switch {
		case rate >= th.BouncePct:
			if v.State != VerdictPause {
				t.Fatalf("rate %.2f%% >= %.2f%%: got %q, want pause", rate, th.BouncePct, v.State)
			}
		case rate >= th.BouncePct*WarnFraction:
			if v.State != VerdictWarn {
				t.Fatalf("rate %.2f%% in warn band: got %q, want warn", rate, v.State)
			}
		default:
			if v.State != VerdictOk {
				t.Fatalf("rate %.2f%% below warn band: got %q, want ok", rate, v.State)
			}
		}
	}
}

// A zero/negative threshold is a corrupted or unset setting. It must fail toward
// "do not pause", not toward "pause every campaign past the sample floor".
func TestZeroThresholdsFallBackToDefaults(t *testing.T) {
	in := Inputs{Delivered: 1000, Bounced: 1, Complained: ptr(0)}
	if v := Breach(in, Thresholds{}); v.State != VerdictOk {
		t.Fatalf("empty thresholds on a 0.1%% bounce rate: got %q, want ok", v.State)
	}
	// And the sample floor still applies when MinDelivered is left unset.
	small := Inputs{Delivered: 10, Bounced: 10}
	if v := Breach(small, Thresholds{BouncePct: 8, ComplaintPct: 1.5}); v.State != VerdictOk {
		t.Fatalf("unset MinDelivered: got %q on a 10-send sample, want ok", v.State)
	}
}

func TestComputeScoreMatrix(t *testing.T) {
	cases := []struct {
		name      string
		in        Inputs
		wantValue int
	}{
		{
			name:      "no evidence at all scores 100 at low confidence",
			in:        Inputs{},
			wantValue: 100,
		},
		{
			name:      "clean campaign",
			in:        Inputs{Delivered: 1000, Bounced: 0},
			wantValue: 100,
		},
		{
			// 5% of 10% saturation * 40 = 20.
			name:      "5% bounce costs half the bounce ceiling",
			in:        Inputs{Delivered: 1000, Bounced: 50},
			wantValue: 80,
		},
		{
			name:      "bounce saturates at 10%",
			in:        Inputs{Delivered: 1000, Bounced: 100},
			wantValue: 100 - BouncePenalty,
		},
		{
			name:      "bounce past saturation costs no more",
			in:        Inputs{Delivered: 1000, Bounced: 900},
			wantValue: 100 - BouncePenalty,
		},
		{
			name:      "complaint saturates at 0.30%",
			in:        Inputs{Delivered: 1000, Bounced: 0, Complained: ptr(3)},
			wantValue: 100 - ComplaintPenalty,
		},
		{
			// 20 spam of 100 observed = 20%, half of the 40% saturation.
			name:      "spam placement scales to observed placement, not delivered",
			in:        Inputs{Delivered: 1000, SpamPlaced: ptr(20), InboxPlaced: ptr(80)},
			wantValue: 100 - SpamPlacementPenalty/2,
		},
		{
			name:      "warmup throttled",
			in:        Inputs{Delivered: 1000, WarmupState: WarmupThrottled},
			wantValue: 100 - WarmupThrottledPenalty,
		},
		{
			name:      "failing domain auth",
			in:        Inputs{Delivered: 1000, DomainState: DomainFailing},
			wantValue: 100 - DomainAuthPenalty,
		},
		{
			name:      "passing domain auth costs nothing",
			in:        Inputs{Delivered: 1000, DomainState: DomainPassing},
			wantValue: 100,
		},
		{
			name:      "unknown domain auth costs nothing",
			in:        Inputs{Delivered: 1000, DomainState: DomainUnknown},
			wantValue: 100,
		},
		{
			// 40 + 30 + 40 + 50 + 20 = 180 penalty. Floors at 0, never negative.
			name: "everything at once floors at zero",
			in: Inputs{
				Delivered: 1000, Bounced: 500, Complained: ptr(100),
				SpamPlaced: ptr(90), InboxPlaced: ptr(10),
				WarmupState: WarmupPaused, DomainState: DomainFailing,
			},
			wantValue: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Compute(c.in).Value; got != c.wantValue {
				t.Errorf("Compute(%+v).Value = %d, want %d", c.in, got, c.wantValue)
			}
		})
	}
}

// Invariant 4: a nil input is EXCLUDED, not zeroed. The score must be identical
// to the no-signal case, and the component must say it was not measured — if nil
// silently scored as clean we would tell operators their complaint rate is fine
// when nobody ever looked.
func TestNilInputIsExcludedNotZeroed(t *testing.T) {
	unmeasured := Compute(Inputs{Delivered: 1000, Complained: nil, SpamPlaced: nil, InboxPlaced: nil})
	measuredClean := Compute(Inputs{Delivered: 1000, Complained: ptr(0), SpamPlaced: ptr(0), InboxPlaced: ptr(10)})

	// Both are penalty-free, so the VALUE cannot distinguish them...
	if unmeasured.Value != 100 || measuredClean.Value != 100 {
		t.Fatalf("values = %d / %d, want 100 / 100", unmeasured.Value, measuredClean.Value)
	}
	// ...which is exactly why `measured` has to.
	for _, key := range []string{KeyComplaint, KeySpamPlacement} {
		absent := component(t, unmeasured, key)
		if absent.Measured {
			t.Errorf("%s: measured=true with a nil input", key)
		}
		if absent.Rate != nil {
			t.Errorf("%s: rate %v reported for an unmeasured signal", key, *absent.Rate)
		}
		if absent.Detail == "" {
			t.Errorf("%s: unmeasured with no explanation for the UI to render", key)
		}
		if present := component(t, measuredClean, key); !present.Measured {
			t.Errorf("%s: measured=false with a real zero observation", key)
		}
	}
}

// Observing zero placements is not observing clean placement: a mailbox with no
// warmup receipts at all has told us nothing.
func TestZeroObservedPlacementIsUnmeasured(t *testing.T) {
	s := Compute(Inputs{Delivered: 1000, SpamPlaced: ptr(0), InboxPlaced: ptr(0)})
	if c := component(t, s, KeySpamPlacement); c.Measured {
		t.Errorf("spam placement measured=true with 0 inbox + 0 spam observed")
	}
}

// With no delivered mail there is no bounce rate — not a 0% one.
func TestBounceUnmeasuredWithoutASample(t *testing.T) {
	s := Compute(Inputs{Delivered: 0, Bounced: 0})
	if c := component(t, s, KeyBounce); c.Measured || c.Rate != nil {
		t.Errorf("bounce component = %+v, want unmeasured with a nil rate", c)
	}
}

func TestConfidence(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Confidence
	}{
		{
			name: "tiny sample is never trustworthy",
			in:   Inputs{Delivered: 10, Bounced: 0, Complained: ptr(0), SpamPlaced: ptr(1), InboxPlaced: ptr(9)},
			want: ConfidenceLow,
		},
		{
			// Bounce alone is one measured signal out of five.
			name: "a single measured signal is low however big the sample",
			in:   Inputs{Delivered: 100000, Bounced: 0},
			want: ConfidenceLow,
		},
		{
			name: "enough sample, some signals missing",
			in:   Inputs{Delivered: 100, Bounced: 1, SpamPlaced: ptr(1), InboxPlaced: ptr(99)},
			want: ConfidenceMedium,
		},
		{
			// Big sample, but still no complaint feed.
			name: "large sample without a complaint feed is not high",
			in:   Inputs{Delivered: 5000, Bounced: 1, SpamPlaced: ptr(1), InboxPlaced: ptr(99)},
			want: ConfidenceMedium,
		},
		{
			name: "large sample with every signal measured",
			in: Inputs{
				Delivered: 5000, Bounced: 1, Complained: ptr(0),
				SpamPlaced: ptr(1), InboxPlaced: ptr(99), WarmupState: WarmupWatch,
			},
			want: ConfidenceHigh,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Compute(c.in).Confidence; got != c.want {
				t.Errorf("confidence = %q, want %q", got, c.want)
			}
		})
	}
}

// The score reports the sample it was computed over, so the UI can render "92
// over 14 delivered" rather than a bare 92.
func TestScoreCarriesItsSample(t *testing.T) {
	for _, delivered := range []int{0, 1, 49, 50, 1000} {
		t.Run(fmt.Sprint(delivered), func(t *testing.T) {
			if got := Compute(Inputs{Delivered: delivered}).Delivered; got != delivered {
				t.Errorf("Delivered = %d, want %d", got, delivered)
			}
		})
	}
}

// An unrecognized warmup state can only come from a direct write bypassing the
// CHECK constraint. Penalising a healthy mailbox on a typo would be worse than
// ignoring it — same rule sendcap.ColdFactor follows.
func TestUnknownWarmupStateIsUnpenalised(t *testing.T) {
	s := Compute(Inputs{Delivered: 1000, WarmupState: "definitely-not-a-state"})
	if s.Value != 100 {
		t.Errorf("score = %d with an unrecognized warmup state, want 100", s.Value)
	}
}

// Every component the score reports must be one of the five keys the frozen API
// schema enumerates: an unknown key renders as nothing in the UI.
func TestComponentKeysMatchTheAPIContract(t *testing.T) {
	want := map[string]bool{
		KeyBounce: true, KeyComplaint: true, KeySpamPlacement: true,
		KeyWarmup: true, KeyDomainAuth: true,
	}
	s := Compute(Inputs{Delivered: 1})
	if len(s.Components) != len(want) {
		t.Fatalf("got %d components, want %d", len(s.Components), len(want))
	}
	for _, c := range s.Components {
		if !want[c.Key] {
			t.Errorf("component key %q is not in the API contract", c.Key)
		}
		if c.Label == "" {
			t.Errorf("component %q has no label", c.Key)
		}
		delete(want, c.Key)
	}
	for k := range want {
		t.Errorf("component %q missing from the score", k)
	}
}
