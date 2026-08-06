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

// frame tracks one currently-open '{' during Expand's single pass: start is
// where it was written into the output buffer, and tainted records whether a
// nested REAL group inside it was left with its own braces literally present
// (because it had no '|', or was itself tainted) — which disqualifies THIS
// frame from spin-resolution too, and propagates further outward if this
// frame also ends up unresolved. A doubled merge field ("{{...}}") never
// creates a frame at all (see mergeFieldSpan) and so never taints anything:
// its braces are invisible to spin-group nesting, even though they remain
// literally present in the output.
type frame struct {
	start   int
	tainted bool
}

// Expand resolves every "{option-a|option-b|...}" spin group in s to ONE
// selected option, chosen deterministically from seed (see Seed). Groups
// resolve innermost-first, in a single left-to-right pass: a '{' opens a
// candidate group and a matching '}' closes the innermost one still open. In
// "{Hi|{Hey|Yo}}" the inner "{Hey|Yo}" closes (and resolves) before the
// outer, so by the time the outer '}' is reached its content is already
// brace-free and it resolves too, on the SAME pass — no rescanning.
//
// A doubled merge field like "{{first_name}}" is recognized up front (see
// mergeFieldSpan) and copied through as one ATOMIC token: its braces never
// open or close a spin group, so a spin option that directly wraps one —
// "{Hi {{first_name}}|Hey {{first_name}}}" — still resolves normally, with
// the chosen option's merge field surviving intact for personalize to
// substitute afterward. That is the whole point of spinning BEFORE
// personalizing.
//
// A brace group with no '|' that is NOT a doubled merge field — a bare
// "{literal}" — is different: it is left byte-for-byte untouched (braces
// included), and because those braces then remain literally in the output,
// an ENCLOSING group that contains one is never judged resolvable either
// (tracked via frame.tainted, not by re-scanning output bytes). That is a
// narrower, deliberate boundary: single-brace literals aren't reserved
// template syntax in this codebase (internal/worker/personalize only ever
// substitutes "{{...}}"), so there is no real-world case this needs to see
// through.
//
// Bounded at maxIterations resolutions total (see its doc) so pathological
// input cannot cost more than a small constant multiple of len(s); the pass
// itself is always a single O(len(s)) scan regardless of nesting depth or
// group count (mergeFieldSpan's look-ahead included — see its doc), so
// Expand's cost never multiplies with the number of groups.
func Expand(s string, seed uint64) string {
	rng := rand.New(rand.NewPCG(seed, pcgSalt))

	out := make([]byte, 0, len(s))
	var stack []frame
	resolved := 0

	for i := 0; i < len(s); i++ {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			if end, ok := mergeFieldSpan(s, i); ok {
				out = append(out, s[i:end]...)
				i = end - 1
				continue
			}
		}
		c := s[i]
		switch {
		case c == '{':
			stack = append(stack, frame{start: len(out)})
			out = append(out, '{')
		case c == '}' && len(stack) > 0:
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			inner := out[f.start+1:]
			if !f.tainted && resolved < maxIterations {
				if options := bytes.Split(inner, []byte("|")); len(options) > 1 {
					choice := options[rng.IntN(len(options))]
					out = append(out[:f.start], choice...)
					resolved++
					continue
				}
			}
			// Left unresolved (no pipe, tainted, or cap reached): this
			// group's braces stay literally in out, so whatever still
			// encloses it now contains a real, unresolved nested group.
			out = append(out, '}')
			if len(stack) > 0 {
				stack[len(stack)-1].tainted = true
			}
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// mergeFieldSpan looks for the doubled-brace merge-field token starting at
// s[i:i+2]=="{{": the nearest subsequent "}}", provided nothing that looks
// like the start of ANOTHER group ('{') appears first. That keeps the match
// to a flat "{{...}}" shape — exactly what internal/worker/personalize
// substitutes ({{first_name}}, {{custom.key}}, ...) — without trying to parse
// anything more exotic between the braces. ok is false for a dangling/
// unclosed "{{" or one that runs into a nested '{' first; the caller then
// falls back to ordinary single-brace handling for both braces of the pair.
//
// Each call either returns in O(1) (the very next character is itself '{',
// the common case inside a long run of genuine nested groups) or scans a
// span it will not be asked to re-scan from this same starting shape, so
// Expand's total cost stays O(len(s)) even under adversarial input built
// from repeated "{{" runs.
func mergeFieldSpan(s string, i int) (end int, ok bool) {
	for j := i + 2; j+1 < len(s); j++ {
		switch {
		case s[j] == '{':
			return 0, false
		case s[j] == '}' && s[j+1] == '}':
			return j + 2, true
		}
	}
	return 0, false
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
// A NUL byte separates consecutive parts in the fold, so ("ab","c") and
// ("a","bc") no longer hash to the same value. This changes Seed's output
// for any multi-part input versus an earlier version of this function — that
// is fine: no seed is ever persisted, and determinism only matters WITHIN a
// retry, which recomputes the seed from the same parts every time regardless
// of how the fold itself is defined.
func Seed(parts ...string) uint64 {
	h := fnv.New64a()
	for i, p := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{0}) // hash.Hash.Write never returns an error.
		}
		_, _ = h.Write([]byte(p))
	}
	return h.Sum64()
}
