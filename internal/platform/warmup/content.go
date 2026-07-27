package warmup

import (
	"context"
	"errors"
	"strings"
)

// ErrNoContent is returned by a generator that has no threads to offer. The
// static library is always populated, so this only guards a misconstructed one.
var ErrNoContent = errors.New("warmup: no content threads available")

// greetingToken is the single light-substitution placeholder in a library
// thread. Keeping substitution to one obvious token keeps the mail natural and
// the output easy to reason about; richer templating can arrive with the AI
// generator without changing this seam.
const greetingToken = "{greeting}"

// greetings are the interchangeable openers substituted for greetingToken. All
// are ordinary, low-key business openers — nothing that reads as bulk or
// marketing mail.
var greetings = []string{"Hi", "Hey", "Hello", "Morning"}

// Thread is a synthetic conversation: an opening message plus the reply turns
// that advance it. Turns[0] is the opener; Turns[1:] are successive replies.
// Subject is shared across the thread (replies prepend "Re:" at send time).
type Thread struct {
	Subject string
	Turns   []string
}

// ContentGenerator is the injected seam that produces warmup conversations. The
// static library is the only implementation today; an AI-backed generator can
// drop in later behind the same interface, exactly as replyclassify's
// ModelClassifier does for classification. Thread must be deterministic in seed
// so a reply simulation can reproduce the same conversation.
type ContentGenerator interface {
	Thread(ctx context.Context, seed string) (Thread, error)
}

// StaticLibrary is a ContentGenerator backed by a fixed, curated set of
// natural-sounding business-email threads. It holds no mutable state and is safe
// for concurrent use.
type StaticLibrary struct {
	threads []Thread
}

// NewStaticLibrary returns the curated static content library.
func NewStaticLibrary() *StaticLibrary {
	return &StaticLibrary{threads: curatedThreads()}
}

// Thread selects a thread deterministically from seed and applies the light
// greeting substitution (also seed-derived), so the same seed always yields the
// same conversation. It returns a fresh copy — the library's own threads are
// never mutated. ctx is accepted to satisfy the seam (an AI generator will use
// it); the static path does no I/O and never cancels.
func (l *StaticLibrary) Thread(_ context.Context, seed string) (Thread, error) {
	if len(l.threads) == 0 {
		return Thread{}, ErrNoContent
	}
	tmpl := l.threads[hashU64("thread", seed)%uint64(len(l.threads))]
	greeting := greetings[hashU64("greeting", seed)%uint64(len(greetings))]

	out := Thread{
		Subject: substitute(tmpl.Subject, greeting),
		Turns:   make([]string, len(tmpl.Turns)),
	}
	for i, turn := range tmpl.Turns {
		out.Turns[i] = substitute(turn, greeting)
	}
	return out, nil
}

// Reply returns the body of the given turn for reply simulation, and ok=false
// once the thread is exhausted (turn out of range). Turn 0 is the opener; the
// engage worker asks for turn 1, 2, … as it advances the conversation.
func Reply(thread Thread, turn int) (string, bool) {
	if turn < 0 || turn >= len(thread.Turns) {
		return "", false
	}
	return thread.Turns[turn], true
}

// MaxContentTurns is the greatest turn count any library thread has. The send
// path passes it to SelectWarmupReplyPartner as a COARSE upper bound: a thread
// whose turn has reached this value is exhausted for EVERY library thread, so it
// can never yield another reply and is excluded from the repliable-partner search.
// The authoritative, per-thread exhaustion check stays warmup.Reply against the
// resolved content (a shorter thread can be exhausted below this bound). Deriving
// it from the static library keeps the bound's single source of truth here rather
// than as a magic number embedded in SQL.
func MaxContentTurns() int {
	longest := 0
	for _, t := range curatedThreads() {
		if len(t.Turns) > longest {
			longest = len(t.Turns)
		}
	}
	return longest
}

func substitute(s, greeting string) string {
	return strings.ReplaceAll(s, greetingToken, greeting)
}

// curatedThreads is the v1 content set: short, mundane, one-to-one business
// exchanges — the kind of low-volume mail a warming mailbox should be trading.
// No links, no offers, no calls to action that read as marketing.
func curatedThreads() []Thread {
	return []Thread{
		{
			Subject: "Quick question on the Q3 numbers",
			Turns: []string{
				"{greeting}, do you have the finalised Q3 figures handy? Trying to close out the summary this afternoon.",
				"Thanks for sending those over. The margin line looks a little different from last month — was there a reclass?",
				"Makes sense, appreciate you walking me through it. I'll fold it into the summary and share back.",
			},
		},
		{
			Subject: "Notes from this morning",
			Turns: []string{
				"{greeting}, jotted down the main points from the standup in case you were pulled away. Nothing urgent.",
				"Good catch on the timeline — I'd forgotten we pushed the review a week. Updated my calendar.",
				"All set on my end. Let's regroup Thursday if anything shifts.",
			},
		},
		{
			Subject: "Draft ready whenever you are",
			Turns: []string{
				"{greeting}, the draft is in reasonable shape now. No rush, but happy to walk through it whenever suits you.",
				"Tomorrow morning works well for me. I'll send a hold so it's on both our calendars.",
				"Perfect, talk then. I'll bring the open questions to that.",
			},
		},
		{
			Subject: "Following up on the vendor call",
			Turns: []string{
				"{greeting}, quick follow-up from the vendor call — did we land on who owns the next step?",
				"Right, that was my read too. I'll take the write-up and loop you in before it goes out.",
				"Thanks. I'll keep an eye out for it and reply with anything that needs a second look.",
			},
		},
		{
			Subject: "Lunch moved to 1",
			Turns: []string{
				"{greeting}, heads up the team lunch slid to 1pm — the earlier room was double-booked.",
				"No problem at all, 1 is actually easier for me. See you there.",
			},
		},
		{
			Subject: "Re-sending the checklist",
			Turns: []string{
				"{greeting}, re-sending the onboarding checklist since I think it got buried. Let me know if anything's unclear.",
				"Got it this time, thanks. Two items I'll need a hand with — I'll flag them once I've had a proper look.",
				"Sounds good. Ping me whenever and we'll sort them together.",
			},
		},
	}
}
