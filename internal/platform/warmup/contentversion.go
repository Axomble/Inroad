package warmup

import (
	"encoding/hex"
	"strconv"
)

// Content-version attribution (network design §8, §12 Phase 2).
//
// A placement observation records WHERE a warmup message landed but not WHICH
// content produced it, so "this thread template lands in spam" and "this mailbox is
// degrading" are the same signal. They call for opposite responses: retire a
// template, or contain a mailbox. This file is the identifier that separates them.
//
// # What it identifies, and why that is a turn
//
// One warmup send transmits ONE turn of one library thread, under the thread's shared
// subject. That pair — (template, turn) — is the content that was measured, so it is
// what the version names. Attributing to the whole thread instead would blend an
// opener that gets filtered with a reply that does not, and in-thread replies land
// differently from cold openers for reasons that have nothing to do with the wording.
//
// # Why the TEMPLATE and not the body that was sent
//
// Library content is spintax: every field expands differently per seed, on purpose
// (identical bodies recurring across a warmup network are a fingerprint an ESP can
// cluster on — see content.go). Fingerprinting the EXPANDED body would therefore mint
// a fresh "version" for nearly every send, each with a sample of one. The template is
// the thing an operator can actually act on: it is what gets edited or retired.
//
// # Why it is content-addressed and not an index
//
// The corpus is meant to GROW, and threads are added in the middle of a slice
// literal. An identifier derived from a thread's position would silently re-label
// every existing thread's history on the next insert: one template's spam rate would
// inherit another template's samples, and nothing would report an error. Hashing the
// template's own text is immune to insertion and reordering, and it changes when — and
// only when — the text an operator is judging actually changes.

// contentVersionScheme names the generator whose content the digest identifies:
// `sl` for the static library, revision 1 of this derivation.
//
// It exists so a future generator (the AI-backed ContentGenerator content.go's seam
// anticipates) gets its own namespace instead of colliding in the same digest space,
// and so a deliberate change to THIS derivation can be introduced as a new scheme
// rather than as a silent rename of every historical row. The persisted CHECK admits
// any scheme, so adding one needs no migration.
const contentVersionScheme = "sl1"

// ContentVersion returns the stable identifier of the content one warmup send
// transmitted: turn `turn` of whichever library thread `contentKey` selects.
// contentKey is warmup_threads.content_key — the seed the send path persists so a
// later reply regenerates the identical conversation.
//
// It returns "" when the resolved thread has no such turn, which the caller records
// as "not attributed" rather than as content. The send path never asks for one
// (warmup.Reply gates the turn first), so this is the defensive arm.
//
// Pure and deterministic: SHA-256 over fixed strings. No clock, no randomness, no map
// iteration, no address, nothing derived from the library's ORDER — so the same
// content yields the same identifier in every process, on every host, forever. That is
// not a nicety: this is a grouping key over a 7-day window, and a value that moved
// across a restart would split one template into many and make every rate meaningless.
func ContentVersion(contentKey string, turn int) string {
	tmpl, ok := contentTemplateFor(contentKey)
	if !ok {
		return ""
	}
	return contentVersionOf(tmpl, turn)
}

// contentTemplateFor resolves the library template a content key selects.
//
// The selection rule is STATED TWICE — here and in StaticLibrary.Thread — and that is
// a deliberate, guarded duplication rather than an oversight. The generator seam
// returns a fully EXPANDED Thread (per content.go's contract), so there is no way to
// ask it "which template did you use" without widening the interface every future
// generator would have to implement, for the benefit of a column that gates nothing.
// TestTheTemplateFingerprintedIsTheTemplateTheLibrarySends pins the two together by
// re-expanding what this function picked and comparing it to what the library
// actually sends, so a drift fails a test instead of silently attributing placement
// to content that was never transmitted.
func contentTemplateFor(contentKey string) (Thread, bool) {
	threads := curatedThreads()
	if len(threads) == 0 {
		return Thread{}, false
	}
	return threads[hashU64("thread", contentKey)%uint64(len(threads))], true
}

// contentVersionOf fingerprints one turn of a template.
//
// The digest covers the subject, the turn's own template text and the turn INDEX. The
// index is in there so two turns that happen to share wording stay distinguishable;
// the sibling turns are deliberately NOT, so rewording turn 2 does not discard the
// accumulated history of turns 1 and 3.
func contentVersionOf(tmpl Thread, turn int) string {
	if turn < 0 || turn >= len(tmpl.Turns) {
		return ""
	}
	digest := hashU64(contentVersionScheme, tmpl.Subject, strconv.Itoa(turn), tmpl.Turns[turn])
	var raw [8]byte
	for i := 7; i >= 0; i-- {
		raw[i] = byte(digest)
		digest >>= 8
	}
	return contentVersionScheme + ":" + hex.EncodeToString(raw[:])
}
