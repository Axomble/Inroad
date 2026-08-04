// Package spintax expands "{option-a|option-b}" spin syntax into ONE
// pseudo-randomly chosen option. It is deliberately deterministic given a
// seed rather than truly random: a retried send job is rebuilt fresh from the
// same immutable inputs (see coreapi.StepSendJob.SendID), and the retry must
// regenerate the byte-identical variant rather than roll a different one —
// the same determinism rule internal/platform/cadence applies to jitter and
// scheduling. Nothing here touches the clock or global math/rand state:
// Expand seeds a fresh, call-scoped math/rand/v2 source from its explicit
// seed argument, so two calls with the same seed always agree and two calls
// with different seeds are independent of each other and of process state.
package spintax

import (
	"bytes"
	"hash/fnv"
	"math/rand/v2"
)

// maxIterations bounds how many spin groups Expand will resolve. Beyond it,
// any remaining "{a|b}" is left exactly as written (literal braces and pipe),
// the same fallback already applied to a group with no '|' — inert, not
// malformed. Chosen generously above any realistic template's group count;
// it exists to bound pathological input (thousands of groups, deep nesting)
// rather than to ever fire on a real campaign step.
const maxIterations = 10000

// pcgSalt is the fixed second half of math/rand/v2's two-part PCG seed. The
// caller-supplied seed (see Seed) fills the first half, so every Expand call
// with the same seed is fully determined by that one argument; the salt just
// keeps this package's draws independent of any other PCG source seeded the
// same way elsewhere in the codebase.
const pcgSalt = 0xda7a

// Expand resolves every "{option-a|option-b|...}" spin group in s to ONE
// selected option, chosen deterministically from seed (see Seed). Groups
// resolve innermost-first, in a single left-to-right pass: a '{' opens a
// candidate group and a matching '}' closes the innermost one still open. In
// "{Hi|{Hey|Yo}}" the inner "{Hey|Yo}" closes (and resolves) before the
// outer, so by the time the outer '}' is reached its content is already
// brace-free and it resolves too, on the SAME pass — no rescanning.
//
// A brace group with no '|' — including the inner span of a doubled merge
// field like "{{first_name}}" — is not spin syntax: it is left byte-for-byte
// untouched, braces included. Because those braces then remain literally in
// the output, an ENCLOSING group that contains one (a spin option directly
// wrapping a "{{merge_field}}" with no separating text) is never judged
// "innermost" either, by the same brace-free-content rule, and so is left
// untouched in turn — a known boundary of this brace-counting algorithm, not
// a bug: it only ever resolves a group whose content is itself free of any
// unresolved nested braces.
//
// Bounded at maxIterations resolutions total (see its doc) so pathological
// input cannot cost more than a small constant multiple of len(s); the pass
// itself is always a single O(len(s)) scan regardless of nesting depth or
// group count, so Expand's cost never multiplies with the number of groups.
func Expand(s string, seed uint64) string {
	rng := rand.New(rand.NewPCG(seed, pcgSalt))

	out := make([]byte, 0, len(s))
	// starts holds, for each '{' currently open, the index into out where it
	// was written. Popping the top on '}' is exactly "the innermost group
	// still open" — LIFO order matches brace nesting order.
	var starts []int
	resolved := 0

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '{':
			starts = append(starts, len(out))
			out = append(out, '{')
		case c == '}' && len(starts) > 0:
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			inner := out[start+1:]
			if resolved < maxIterations && !bytes.ContainsAny(inner, "{}") {
				if options := bytes.Split(inner, []byte("|")); len(options) > 1 {
					choice := options[rng.IntN(len(options))]
					out = append(out[:start], choice...)
					resolved++
					continue
				}
			}
			out = append(out, '}')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// Seed derives a deterministic uint64 from parts via FNV-1a, for Expand.
// Call sites key each field's draw on Seed(sendID, fieldName) — e.g.
// Seed(job.SendID, "subject") — so a retried job (same deterministic SendID,
// same field name) reproduces the identical spin choice instead of re-rolling
// and drifting from content that may already be in flight. Using a distinct
// fieldName per field ("subject" vs "body_text" vs "body_html") gives each
// field its own independent draw: resolving the subject's option does not
// determine the body's.
//
// FNV-1a is applied over the parts with no separator between them, so two
// different part splits that happen to concatenate to the same bytes (e.g.
// ("ab","c") vs ("a","bc")) would collide. This is safe for every actual call
// site in this codebase: the first part is always a fixed-format UUID
// (SendID or a step id), so no other real part sequence can concatenate to
// the same bytes.
func Seed(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p)) // hash.Hash.Write never returns an error.
	}
	return h.Sum64()
}
