package warmup

import (
	"context"
	"errors"
	"strconv"

	"github.com/inroad/inroad/internal/platform/spintax"
)

// ErrNoContent is returned by a generator that has no threads to offer. The
// static library is always populated, so this only guards a misconstructed one.
var ErrNoContent = errors.New("warmup: no content threads available")

// greeting is the shared opener spin group every thread's first turn starts with.
// Holding it in one place means adding an opener widens the ENTIRE corpus at once
// instead of one thread at a time. All options are ordinary, low-key business
// openers — nothing that reads as bulk or marketing mail.
const greeting = "{Hi|Hey|Hello|Morning}"

// Thread is a synthetic conversation: an opening message plus the reply turns
// that advance it. Turns[0] is the opener; Turns[1:] are successive replies.
// Subject is shared across the thread (replies prepend "Re:" at send time).
//
// In the library's own templates both Subject and every turn carry spintax spin
// groups; the Thread a generator RETURNS is always fully expanded, with no spin
// syntax left in it.
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

// StaticLibrary is a ContentGenerator backed by a curated set of natural-sounding
// business-email threads, each written as a spintax template so the effective
// corpus is combinatorial rather than a handful of fixed bodies. It holds no
// mutable state and is safe for concurrent use.
type StaticLibrary struct {
	threads []Thread
}

// NewStaticLibrary returns the curated static content library.
func NewStaticLibrary() *StaticLibrary {
	return &StaticLibrary{threads: curatedThreads()}
}

// Thread selects a template deterministically from seed and expands its spintax,
// so the same seed always yields the same conversation while different seeds spread
// across the whole combinatorial corpus. It returns a fresh copy — the library's own
// templates are never mutated. ctx is accepted to satisfy the seam (an AI generator
// will use it); the static path does no I/O and never cancels.
//
// Each field draws on its own spintax.Seed namespace, exactly as the campaign send
// path does: the subject's chosen variant does not determine the opener's, and turn
// 3's does not determine turn 1's, so the variation multiplies across the thread
// instead of collapsing to one draw per conversation. spintax is seed-deterministic
// by construction (see its package doc), which is why it can be used here without
// giving up the reproducibility a replayed reply simulation depends on.
func (l *StaticLibrary) Thread(_ context.Context, seed string) (Thread, error) {
	if len(l.threads) == 0 {
		return Thread{}, ErrNoContent
	}
	tmpl := l.threads[hashU64("thread", seed)%uint64(len(l.threads))]

	out := Thread{
		Subject: spintax.Expand(tmpl.Subject, spintax.Seed(seed, "subject")),
		Turns:   make([]string, len(tmpl.Turns)),
	}
	for i, turn := range tmpl.Turns {
		out.Turns[i] = spintax.Expand(turn, spintax.Seed(seed, "turn", strconv.Itoa(i)))
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
//
// Spintax expansion cannot invalidate this bound: a spin group lives entirely within
// one turn, so expanding a template rewrites turn CONTENT and never changes how many
// turns it has. len(tmpl.Turns) is therefore the same before and after expansion.
func MaxContentTurns() int {
	longest := 0
	for _, t := range curatedThreads() {
		if len(t.Turns) > longest {
			longest = len(t.Turns)
		}
	}
	return longest
}

// curatedThreads is the content set: short, mundane, one-to-one business exchanges
// — the kind of low-volume mail a warming mailbox should be trading. The editorial
// constraints are deliberate and must hold for anything added here: NO links, NO
// offers, no calls to action, nothing that reads as marketing, bulk, or a sequence
// step. Two colleagues sorting out an invoice code is the target register.
//
// Every field is a spintax template. That matters beyond variety: identical bodies
// recurring across a warmup network are a fingerprint an ESP can cluster on, so the
// corpus has to expand combinatorially rather than cycle through a handful of fixed
// strings. Widening an existing group multiplies the corpus; adding a thread only
// adds to it — so prefer more alternatives inside a thread over more threads.
//
// Turn COUNTS are fixed per template (spin groups never span a turn boundary), which
// is what keeps MaxContentTurns a valid bound over the expanded corpus.
func curatedThreads() []Thread {
	return []Thread{
		{
			Subject: "{Quick question on the Q3 numbers|Q3 numbers — quick question|Question on the Q3 figures}",
			Turns: []string{
				greeting + ", do you have the {finalised|final|signed-off} Q3 {figures|numbers} handy? {Trying to close out|Hoping to close out|Wanting to wrap} the summary {this afternoon|today|before end of day}.",
				"Thanks for {sending those over|digging those out|the quick turnaround}. The margin line looks {a little|slightly} different from last month — {was there a reclass|did something get reclassed|any reclass I should know about}?",
				"{Makes sense|That explains it|Got it}, {appreciate you walking me through it|thanks for talking me through it}. I'll fold it into the summary and {share it back|send it round}.",
			},
		},
		{
			Subject: "{Notes from this morning|This morning's notes|Standup notes}",
			Turns: []string{
				greeting + ", {jotted down|wrote up|captured} the main points from {the standup|this morning's standup|the sync} in case you {were pulled away|had to drop|missed the end}. {Nothing urgent|No action needed|Nothing that needs a reply}.",
				"{Good catch|Nice catch|Thanks for flagging} the timeline — I'd forgotten we {pushed the review a week|moved the review out a week}. {Updated my calendar|Calendar updated|I've fixed my calendar}.",
				"All {set|good} on my end. Let's {regroup|pick it up again} {Thursday|later in the week} if anything shifts.",
			},
		},
		{
			Subject: "{Draft ready whenever you are|The draft's ready when you are|Draft is ready}",
			Turns: []string{
				greeting + ", the draft is in {reasonable|decent|reasonably good} shape now. {No rush|No urgency at all}, but happy to {walk through it|talk it through} whenever {suits you|works for you}.",
				"{Tomorrow morning|Tomorrow AM|Wednesday morning} works {well for me|for me}. I'll {send a hold|put a hold in} so it's on both {our calendars|calendars}.",
				"{Perfect|Great}, talk then. I'll bring the {open questions|remaining questions} to that.",
			},
		},
		{
			Subject: "{Following up on the vendor call|Vendor call follow-up|After the vendor call}",
			Turns: []string{
				greeting + ", quick follow-up from {the vendor call|yesterday's vendor call|the call with the vendor} — did we {land on|settle|actually agree} who {owns|picks up} the next step?",
				"{Right|Yes|That's right}, that was my read too. I'll take the {write-up|summary|first pass} and {loop you in|share it with you} before it goes out.",
				"Thanks. I'll {keep an eye out for it|watch for it|look out for it} and {reply with|flag} anything that {needs a second look|needs another pass}.",
				"{Nothing further from me|Nothing else from my side} — {thanks for picking that up|thanks for taking that on}.",
			},
		},
		{
			Subject: "{Lunch moved to 1|Team lunch moved to 1|Lunch is now 1pm}",
			Turns: []string{
				greeting + ", heads up the {team lunch|lunch} {slid to|moved to|has shifted to} 1pm — the earlier room {was double-booked|got double-booked}.",
				"No {problem at all|trouble}, 1 is actually easier for me. {See you there|See you then}.",
			},
		},
		{
			Subject: "{Re-sending the checklist|Checklist, again|Resending the onboarding checklist}",
			Turns: []string{
				greeting + ", re-sending the onboarding checklist since I think it {got buried|slipped down your inbox|got lost in the thread}. {Let me know if anything's unclear|Shout if anything's unclear|Tell me if any of it doesn't make sense}.",
				"{Got it this time|Found it, thanks|Have it now}, thanks. {Two|A couple of} items I'll need a hand with — I'll flag them once I've {had a proper look|read it properly}.",
				"{Sounds good|That works}. {Ping me whenever|Grab me whenever} and we'll {sort them together|work through them together}.",
			},
		},
		{
			Subject: "{Invoice reference|Reference on that invoice|Quick one on the invoice}",
			Turns: []string{
				greeting + ", {do you happen to know|any idea} which {cost centre|cost code} the {March invoice|last invoice} should go against? {Finance kicked it back to me|Finance sent it back|Finance has queried it}.",
				"{That's the one|Perfect, that's it}, thanks — {I'll resubmit it this afternoon|resubmitting now|I'll push it through today}.",
				"{Went through fine|It cleared|All processed}. {Thanks for the quick answer|Thanks for turning that round quickly}.",
			},
		},
		{
			Subject: "{Meeting room for Thursday|Room for Thursday|Booking a room for Thursday}",
			Turns: []string{
				greeting + ", {are you able to|could you} book {a room|the small room|one of the rooms} for Thursday {afternoon|after lunch}? {Four of us|Five of us|Just four of us}, {an hour should do|an hour is plenty}.",
				"{Booked|Done|All booked} — {the small one on the second floor|the room by the stairs} from {2 til 3|2 to 3}. {Invite's out|I've sent the invite}.",
				"{Great, thank you|Perfect, thanks}. I'll {bring the printouts|print the handouts}.",
			},
		},
		{
			Subject: "{Cover while I'm out|Out Thursday and Friday|Away later this week}",
			Turns: []string{
				greeting + ", I'm out {Thursday and Friday|the back half of the week|from Thursday}. {Nothing pressing|Nothing outstanding}, but {could you keep half an eye on|would you mind watching} the {shared inbox|team inbox}?",
				"{Of course|No problem|Happy to}. {Anything I should look out for in particular|Anything specific to watch for}?",
				"{Just the supplier thread|Only the supplier thread} really — {everything else can wait|the rest can wait until I'm back}.",
				"{Understood|Noted}. {Have a good break|Enjoy the time off}.",
			},
		},
		{
			Subject: "{Correction to what I sent|Small correction|Ignore my last one}",
			Turns: []string{
				greeting + ", {small correction to|apologies, a correction to} what I sent {earlier|this morning} — the {second figure|middle column} {should read|should have been} {last quarter's|the prior quarter's}, not this one.",
				"{No harm done|Not a problem|Easy mistake} — I hadn't {got to it yet|opened it yet} anyway. {Is the rest right|Does the rest still hold}?",
				"{Yes|It does}, {the rest is fine|everything else is correct}. {Thanks for checking|Thanks for being patient with it}.",
			},
		},
		{
			Subject: "{Second interview slot|Slot for the second interview|Second round timing}",
			Turns: []string{
				greeting + ", {when are you free|what's your availability|when suits you} next week for the second {interview|round|conversation}? {Half an hour is fine|Thirty minutes is plenty|Half an hour should do it}.",
				"{Tuesday or Wednesday|Tuesday and Wednesday|Either Tuesday or Wednesday} {both work|are both open|work} for me, {any time after 10|anything after 10|after 10 ideally}.",
				"{Tuesday at 11 then|Let's say Tuesday at 11|Tuesday 11 it is}. {Invite sent|I've sent the invite|Invite's in your calendar}.",
			},
		},
		{
			Subject: "{Where the signed copy lives|Signed copy|Can't find the signed copy}",
			Turns: []string{
				greeting + ", {do you know where|any idea where} the signed copy {ended up|got filed|lives}? {I've looked in the obvious places|Not in the usual folder|I've checked the usual folder}.",
				"{It's in the archive folder|Try the archive folder|Should be in the archive folder}, {under last year|filed under last year}. {Easy to miss|Not obvious, I know}.",
				"{Found it|There it is|Got it}, thanks. {I'll leave a note so the next person doesn't hunt for it|I'll add a pointer in the main folder}.",
			},
		},
	}
}
