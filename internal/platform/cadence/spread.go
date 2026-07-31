package cadence

import (
	"slices"
	"strconv"
	"time"
)

// Jitter bounds for send spacing. Wide enough that consecutive gaps are visibly
// unequal, tight enough that the batch still finishes inside its window.
const (
	jitterMin = 0.55
	jitterMax = 1.45
)

// dayCurve is a piecewise-linear weight over the open window, sampled at equal
// fractions from window open to window close. Real senders don't emit at a flat
// rate: volume builds through the morning, dips over lunch, recovers in the early
// afternoon, and tails off before close. Offsets draws through this curve's
// inverse CDF, so the *shape* of a day's sending is uneven in the same way — a
// flat rate across a 9-to-5 window is itself a fingerprint.
//
// Weights are relative; only their ratios matter.
var dayCurve = []float64{0.55, 1.00, 1.35, 1.30, 0.85, 0.70, 1.15, 1.20, 0.95, 0.60}

// Spacing is the delay between the index-th and the next send when `target`
// sends are spread across `open` of window time. It applies a deterministic
// multiplicative jitter in [0.55, 1.45) keyed on (key, index), so no two gaps in
// a run are equal and a retry recomputes the same gap.
//
// Returns 0 when there is nothing to space (non-positive target or open time).
func Spacing(open time.Duration, target, index int, key string) time.Duration {
	if target <= 0 || open <= 0 {
		return 0
	}
	base := open / time.Duration(target)
	jitter := scale(hashU64("spacing", key, strconv.Itoa(index)), jitterMin, jitterMax)
	return time.Duration(float64(base) * jitter)
}

// Offsets returns n ascending offsets into `open`, distributed through dayCurve
// rather than uniformly, and jittered within their own slot. The result is
// sorted ascending so a batch keeps a stable order, and every offset is strictly
// less than open so none spills past the window.
//
// Deterministic in (key, n, open): the same launch recomputes the same schedule,
// which is what lets a retried launch re-stamp identical due times.
func Offsets(open time.Duration, n int, key string) []time.Duration {
	if n <= 0 || open <= 0 {
		return nil
	}
	cdf := curveCDF(dayCurve)
	out := make([]time.Duration, 0, n)
	for i := range n {
		// Sample the slot's midpoint, nudged inside the slot by a deterministic
		// jitter, so n sends don't sit on n evenly-spaced quantiles.
		slot := (float64(i) + scale(hashU64("offset", key, strconv.Itoa(i)), 0.05, 0.95)) / float64(n)
		frac := invCDF(cdf, slot)
		off := time.Duration(frac * float64(open))
		if off >= open {
			off = open - time.Millisecond
		}
		out = append(out, off)
	}
	slices.Sort(out)
	return out
}

// curveCDF turns relative weights into a normalized cumulative distribution with
// len(weights)+1 points, cdf[0] = 0 and cdf[len] = 1.
func curveCDF(weights []float64) []float64 {
	cdf := make([]float64, len(weights)+1)
	var total float64
	for i, w := range weights {
		total += w
		cdf[i+1] = total
	}
	if total <= 0 {
		// Degenerate curve: fall back to uniform rather than dividing by zero.
		for i := range cdf {
			cdf[i] = float64(i) / float64(len(weights))
		}
		return cdf
	}
	for i := range cdf {
		cdf[i] /= total
	}
	return cdf
}

// invCDF maps u in [0,1] back to a position in [0,1) along the curve, linearly
// interpolating inside the bucket u lands in. Sampling uniformly in u and
// reading off invCDF yields values distributed according to the curve's weights.
func invCDF(cdf []float64, u float64) float64 {
	u = clamp(u, 0, 1)
	buckets := len(cdf) - 1
	for i := range buckets {
		lo, hi := cdf[i], cdf[i+1]
		if u > hi {
			continue
		}
		span := hi - lo
		if span <= 0 {
			return float64(i) / float64(buckets)
		}
		within := (u - lo) / span
		return (float64(i) + within) / float64(buckets)
	}
	return 1
}

// scale maps a hash to a float in [lo, hi).
func scale(h uint64, lo, hi float64) float64 {
	// 1<<53 keeps the ratio exactly representable as a float64.
	const denom = 1 << 53
	return lo + (hi-lo)*(float64(h%denom)/float64(denom))
}

func clamp(v, lo, hi float64) float64 {
	return min(max(v, lo), hi)
}
