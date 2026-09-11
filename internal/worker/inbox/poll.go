package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/mime"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/replyclassify"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// fetchBatchSize bounds how many messages one inbox:poll pass pulls from a
// mailbox, mirroring mail.InboxReader.Fetch's maxN contract (bounds the IMAP
// request itself, not just the returned slice).
const fetchBatchSize = 200

// GmailFetcher polls a Gmail mailbox for new inbound messages via the Gmail API,
// resuming from an opaque historyId cursor. *mail.GmailReader satisfies it; the
// worker depends on the interface so it can be unit-tested with a fake (the
// concrete reader's wire seam is unexported). It is the provider-parallel of
// mail.InboxReader for the API transport.
type GmailFetcher interface {
	Fetch(ctx context.Context, accessToken, sinceHistoryID string, maxN int) (msgs []mail.InboundMessage, newCursor string, err error)
}

// GraphFetcher polls an M365 mailbox for new inbound messages via the Microsoft
// Graph delta query, resuming from an opaque delta/next-link URL cursor.
// *mail.GraphReader satisfies it; the worker depends on the interface so it can
// be unit-tested with a fake (the concrete reader's wire seam is unexported). It
// is the provider-parallel of GmailFetcher for the Graph API transport.
type GraphFetcher interface {
	Fetch(ctx context.Context, accessToken, sinceCursor string, maxN int) (msgs []mail.InboundMessage, newCursor string, err error)
}

// WarmupEngageEnqueuer enqueues a delayed warmup:engage task for a detected
// warmup receipt. *queue.Client satisfies it (EnqueueWarmupEngageIn); the poll
// handler depends on the interface so the receipt-detection hook is unit-testable
// with a spy that records the enqueue without touching Redis.
type WarmupEngageEnqueuer interface {
	EnqueueWarmupEngageIn(ctx context.Context, receiptID, workspaceID string, d time.Duration) error
}

// imapJunkScanner is the OPTIONAL junk-folder capability of an IMAP InboxReader:
// a best-effort scan of the mailbox's spam/junk folder for warmup mail (spec §7).
// *mail.NetInboxReader implements it; a reader that does not (e.g. a test fake
// that only cares about INBOX) is simply scanned INBOX-only. Kept separate from
// mail.InboxReader (interface segregation) so the core poll path never depends on
// junk support.
type imapJunkScanner interface {
	FetchJunk(ctx context.Context, cfg mail.IMAPConfig, maxN int) (msgs []mail.InboundMessage, folder string, err error)
}

// gmailSpamScanner is the OPTIONAL SPAM-label capability of a GmailFetcher.
type gmailSpamScanner interface {
	FetchSpam(ctx context.Context, accessToken string, maxN int) ([]mail.InboundMessage, error)
}

// graphJunkScanner is the OPTIONAL JunkEmail-folder capability of a GraphFetcher.
type graphJunkScanner interface {
	FetchJunk(ctx context.Context, accessToken string, maxN int) ([]mail.InboundMessage, error)
}

// apiJunkScan is a bound best-effort junk/spam scan for an API provider: Gmail's
// SPAM label or M365's JunkEmail folder, unified behind one signature so pollAPI
// can drive either. nil when the reader lacks the capability (scan skipped).
type apiJunkScan func(ctx context.Context, accessToken string, maxN int) ([]mail.InboundMessage, error)

// junkScanBatch bounds how many of a spam/junk folder's most-recent messages one
// poll scans for warmup mail. Warmup traffic is recent + low-volume, so a modest
// cap keeps the stateless, idempotent rescan cheap.
const junkScanBatch = 100

// Placement values recorded on a warmup receipt (spec §3). These exact strings
// match the warmup_receipts.placement CHECK constraint (widened by migration
// 000060 for tabbed).
//
// placementTabbed is recorded ONLY when a provider positively identifies a tab.
// `inbox` deliberately keeps meaning "landed in the inbox" rather than being
// redefined as "primary": one column cannot mean "primary inbox" on Gmail and
// "inbox, tab unknowable" on IMAP, differing by a provider the reader does not
// record.
const (
	placementInbox  = "inbox"
	placementTabbed = "tabbed"
	placementSpam   = "spam"
)

// providerGmail is the one provider whose reader can observe a tab. m365 is
// excluded on purpose: Graph's inferenceClassification (focused|other) is a
// per-user RELEVANCE guess, not a delivery category, and IMAP has no concept of a
// tab at all.
const providerGmail = "gmail"

// sourceFolderInbox is the canonical source-folder label for a message found in
// the primary inbox across all providers (IMAP "INBOX", the Gmail INBOX label,
// the Graph Inbox folder), so C5b's engager can act on it uniformly.
const sourceFolderInbox = "INBOX"

// readingPath is what ONE poll pass can say about a message it found: which folder
// it was scanning (hence the placement), the provider's label for that folder, and
// whether that path could have identified a tab at all.
//
// tabCapable lives HERE, on the path, and is recorded on the observation — not
// derived later from mailboxes.provider. A mailbox migrated between providers would
// otherwise make historical observations retroactively claim a capability the
// reader that wrote them never had.
type readingPath struct {
	folderPlacement string
	sourceFolder    string
	tabCapable      bool
}

// inboxPath is the INBOX pass of any provider.
func inboxPath(tabCapable bool) readingPath {
	return readingPath{folderPlacement: placementInbox, sourceFolder: sourceFolderInbox, tabCapable: tabCapable}
}

// junkPath is the spam/junk scan, whose folder label varies by provider (Junk /
// SPAM / JunkEmail) so the engager can act on the exact message.
func junkPath(folder string, tabCapable bool) readingPath {
	return readingPath{folderPlacement: placementSpam, sourceFolder: folder, tabCapable: tabCapable}
}

// providerTabCapable reports whether this provider's reader could have identified a
// tab. It is asked ONCE per poll pass, from the path that is about to read, so the
// answer is recorded alongside the evidence rather than re-derived from the mailbox
// row afterwards.
func providerTabCapable(provider string) bool { return provider == providerGmail }

// warmupPlacement resolves the placement to record from the folder the path was
// scanning and the tab the provider named.
//
// The folder wins: a spam-foldered message is not in the inbox at all, so no tab
// applies to it however the provider also categorised it. The Gmail reader already
// clears the category for a SPAM-labelled message, and this does not rely on that —
// the two guards are independent, and the one that decides the recorded value is
// this one.
func warmupPlacement(path readingPath, category string) string {
	if path.folderPlacement != placementInbox {
		return path.folderPlacement
	}
	// A tab is only recordable by a path that could SEE tabs. The database refuses
	// ('tabbed', tab_capable=false) — and that refusal is expensive: the CHECK aborts
	// the receipt transaction, recordWarmup propagates, and the poll returns BEFORE
	// SetInboxCursor, so the message is re-fetched and the pass fails identically
	// forever. Every inbound signal for that mailbox stops advancing — campaign
	// replies and bounce detection included, not just one lost observation.
	//
	// The capability and the category used to be decided in different places from
	// different inputs, with no compile-time link, so constructing that row was a
	// plain Go bug away. Deciding both here makes the pairing local and the
	// impossible row unconstructable rather than merely rejected.
	if category != "" && path.tabCapable {
		return placementTabbed
	}
	return placementInbox
}

// warmupHook carries the warmup receipt-detection dependencies threaded through
// the poll path: the HMAC secret that verifies the X-Inroad-Warmup token, the
// enqueuer for the delayed warmup:engage follow-up, and who the polled mailbox IS.
// It is the seam that keeps warmup mail ISOLATED from campaign reply/bounce
// classification (spec §9.4).
//
// The receiver lives HERE rather than on readingPath, though both describe the
// reader, because the two have different lifetimes: a readingPath is built per
// folder pass (INBOX, then junk) while the receiver is one fact about the mailbox
// for the whole poll. Putting a per-poll constant in a per-pass struct would imply
// it could differ between passes, and it cannot.
//
// The stronger reason is what it would sit NEXT TO. readingPath.tabCapable is a
// property of the READER and deliberately not derived from the mailbox's provider
// (see its comment). Placing receiver.Provider beside it would put the exact
// derivation that comment forbids within arm's reach of anyone editing the struct
// — and the IMAP branch, which passes tabCapable=false literally, is where that
// mistake has already been made once.
type warmupHook struct {
	secret   []byte
	enq      WarmupEngageEnqueuer
	receiver warmup.Receiver
}

// PollHandler returns an asynq handler for inbox:poll tasks. It dispatches on
// the mailbox provider: gmail polls via the Gmail API (opaque historyId cursor),
// m365 polls via the Microsoft Graph delta query (opaque delta-link cursor),
// smtp opens the mailbox's IMAP connection, establishes/validates the poll
// baseline via CurrentState, and fetches anything new since the stored UID
// cursor.
//
// Before campaign classification, every inbound message runs the warmup
// receipt-detection HOOK (processInbound): a warmup message for this workspace —
// recognised by its verified X-Inroad-Warmup token, or, when the provider stripped
// the header, by matching a warmup send we made to this mailbox (inspectWarmup) —
// is recorded + engaged + STOPPED, never reaching reply/bounce classification
// (spec §9.4 isolation), so warmup traffic can never stop, suppress, or bounce a
// real campaign enrollment. Everything else falls through
// to the SAME reply/bounce classification (processMessage) unchanged. Each path
// also best-effort scans the provider's spam/junk folder for spam-placed warmup
// mail (the core deliverability health signal) and persists its cursor.
func PollHandler(core coreapi.Client, reader mail.InboxReader, gmail GmailFetcher, graph GraphFetcher, classifier *replyclassify.Classifier, warmupSecret []byte, enq WarmupEngageEnqueuer) func(context.Context, *asynq.Task) error {
	// Resolve the optional evidence capability ONCE, at wiring time, and say so
	// loudly if it is missing. Discovering it per message meant a core that does not
	// implement it degraded in silence — and the consequence is worse than lost
	// evidence: the warmup DSN then falls through to FindSendByMessageID/MarkBounced
	// and bounces a CAMPAIGN enrollment, inverting the isolation this hook exists to
	// enforce. A signature change has already caused exactly that failure once.
	if _, ok := core.(coreapi.WarmupEvidenceClient); !ok {
		slog.Error("inbox_poll_warmup_evidence_unavailable",
			"impact", "warmup token failures and warmup DSNs will not be recorded, "+
				"and warmup DSNs may be misclassified as campaign bounces")
	}
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPollPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		job, err := core.GetInboxPollJob(ctx, p.MailboxID, p.WorkspaceID)
		if err != nil {
			return err
		}

		// Built per poll, inside the closure, because the receiver is per mailbox
		// while the secret and the enqueuer are per process. Assembling it once at
		// wiring time and assigning the receiver here would mutate a value shared by
		// every concurrent poll — a data race that would attribute one mailbox's
		// identity verdicts to another's mail.
		//
		// job.Provider is already the vocabulary warmup.Receiver expects
		// ("gmail"|"m365"|"smtp") on every branch below, and job.Email is the mailbox's
		// own address on every branch too — deliberately not job.Username, which is the
		// IMAP login and empty for exactly the providers that stamp results.
		hook := warmupHook{
			secret: warmupSecret, enq: enq,
			receiver: warmup.Receiver{Address: job.Email, Provider: job.Provider},
		}

		if job.Provider == "gmail" {
			return pollAPI(ctx, core, gmail, classifier, hook, p, job, "gmail", gmailJunkScan(gmail))
		}
		if job.Provider == "m365" {
			return pollAPI(ctx, core, graph, classifier, hook, p, job, "m365", graphJunkScan(graph))
		}
		defer zeroize(job.Password)

		cfg := mail.IMAPConfig{Host: job.Host, Port: job.Port, Username: job.Username, Password: string(job.Password)}

		uidValidity, uidNext, err := reader.CurrentState(ctx, cfg)
		if err != nil {
			return err
		}

		// Re-baseline on a first poll (never-polled mailbox, UIDValidity==0)
		// or a UIDVALIDITY reset (the server renumbered the mailbox — old UIDs
		// are meaningless): jump the cursor to the current top and process
		// nothing this pass. This also keeps a mailbox's pre-existing inbox
		// from being treated as a flood of replies the first time it's polled.
		if job.UIDValidity == 0 || uidValidity != job.UIDValidity {
			// RFC 3501 guarantees UIDNEXT > 0, but a misbehaving server must
			// never be able to underflow this uint32 and wedge the mailbox's
			// cursor at math.MaxUint32.
			var base uint32
			if uidNext > 0 {
				base = uidNext - 1
			}
			return core.SetInboxCursor(ctx, p.MailboxID, p.WorkspaceID, base, uidValidity)
		}

		msgs, _, err := reader.Fetch(ctx, cfg, job.LastSeenUID, fetchBatchSize)
		if err != nil {
			return err
		}

		var replies, bounces, skipped int
		// Literal false, not providerTabCapable(job.Provider): this branch is the IMAP
		// transport, which cannot report a tab whatever the provider column says.
		// Deriving it from the provider read as though Gmail-over-IMAP would be
		// tab-capable — and if that route were ever added, every inbox landing would
		// be counted tab-capable while the reader could never produce a numerator, so
		// the tabbed rate would pin at a confident 0%. That is the "untested pool
		// reads clean" failure the separate denominator exists to prevent.
		path := inboxPath(false)
		for _, msg := range msgs {
			matched, err := processInbound(ctx, core, classifier, hook, p, msg, path, &replies, &bounces)
			if err != nil {
				return err
			}
			if !matched {
				skipped++
			}
		}

		// Best-effort spam-placement scan (spec §7): a warmup message that landed
		// in Junk is a spam-placement signal. Runs AFTER the INBOX pass and never
		// fails the poll — the scan is stateless + idempotent, so a junk hiccup is
		// retried on the next poll without holding back the INBOX cursor advance.
		if js, ok := reader.(imapJunkScanner); ok {
			scanIMAPJunk(ctx, core, hook, js, cfg, p, path.tabCapable)
		}

		slog.Info("inbox_poll_processed", "mailbox_id", p.MailboxID,
			"messages", len(msgs), "replies", replies, "bounces", bounces, "skipped", skipped)
		return core.SetInboxCursor(ctx, p.MailboxID, p.WorkspaceID, scannedWindowTop(job.LastSeenUID, uidNext), uidValidity)
	}
}

// scannedWindowTop is the highest UID a successful bounded Fetch(sinceUID,
// fetchBatchSize) has definitively examined — sinceUID+fetchBatchSize,
// capped at the mailbox's current head (uidNext-1, guarded against a
// misbehaving uidNext==0). The cursor always advances to this value after a
// successful fetch+process pass, regardless of how many messages actually
// existed in that range: a UID absent from the range (expunged or never
// assigned) is a gap, not unprocessed mail, so leaving the cursor at the old
// max-processed-UID (or unmoved, on an empty batch) would re-scan the same
// stalled window forever while newer mail sits above it, silently killing
// detection for that mailbox.
func scannedWindowTop(sinceUID, uidNext uint32) uint32 {
	var head uint32
	if uidNext > 0 {
		head = uidNext - 1
	}
	top := sinceUID + uint32(fetchBatchSize)
	if top > head {
		top = head
	}
	return top
}

// apiFetcher is the common shape of GmailFetcher and GraphFetcher: both resume
// from an opaque provider cursor and return the advanced cursor. pollAPI runs
// the shared per-pass logic for either transport.
type apiFetcher interface {
	Fetch(ctx context.Context, accessToken, sinceCursor string, maxN int) (msgs []mail.InboundMessage, newCursor string, err error)
}

// pollAPI runs one inbox poll pass for an API-based mailbox (gmail or m365):
// fetch new messages since the opaque provider cursor, run the warmup hook +
// SAME processMessage classification path as IMAP (processInbound), best-effort
// scan the provider's spam folder for warmup mail (junkScan), and persist the
// advanced cursor via SetInboxCursorString (the IMAP UID cursor columns are
// untouched). The short-lived access token is zeroized after the pass, like the
// IMAP password. Only the transport (reader), the "provider" log value, and the
// junk scanner differ between gmail and m365, so both providers share this body.
func pollAPI(ctx context.Context, core coreapi.Client, reader apiFetcher, classifier *replyclassify.Classifier, hook warmupHook, p queue.InboxPollPayload, job coreapi.InboxPollJob, provider string, junkScan apiJunkScan) error {
	defer zeroize(job.AccessToken)

	msgs, newCursor, err := reader.Fetch(ctx, string(job.AccessToken), job.Cursor, fetchBatchSize)
	if err != nil {
		return err
	}

	var replies, bounces, skipped int
	tabCapable := providerTabCapable(provider)
	for _, msg := range msgs {
		matched, err := processInbound(ctx, core, classifier, hook, p, msg, inboxPath(tabCapable), &replies, &bounces)
		if err != nil {
			return err
		}
		if !matched {
			skipped++
		}
	}

	// Best-effort spam-placement scan (spec §7), same isolation + no-fail policy
	// as the IMAP path. Skipped when the provider reader lacks the capability
	// (junkScan == nil).
	if junkScan != nil {
		scanAPIJunk(ctx, core, hook, junkScan, string(job.AccessToken), provider, p, tabCapable)
	}

	slog.Info("inbox_poll_processed", "mailbox_id", p.MailboxID, "provider", provider,
		"messages", len(msgs), "replies", replies, "bounces", bounces, "skipped", skipped)
	return core.SetInboxCursorString(ctx, p.MailboxID, p.WorkspaceID, newCursor)
}

// gmailJunkScan binds a GmailFetcher's optional SPAM-label scan to the apiJunkScan
// shape, or nil if the reader lacks it (INBOX-only).
func gmailJunkScan(g GmailFetcher) apiJunkScan {
	if s, ok := g.(gmailSpamScanner); ok {
		return s.FetchSpam
	}
	return nil
}

// graphJunkScan binds a GraphFetcher's optional JunkEmail scan to the apiJunkScan
// shape, or nil if the reader lacks it (INBOX-only).
func graphJunkScan(g GraphFetcher) apiJunkScan {
	if s, ok := g.(graphJunkScanner); ok {
		return s.FetchJunk
	}
	return nil
}

// warmupDetection is the verdict on whether one inbound message is warmup mail
// for the polled mailbox's workspace. Only warmupValid is treated as warmup; every
// other verdict falls through to normal reply/bounce classification UNCHANGED
// (spec §9.3).
type warmupDetection uint8

const (
	warmupAbsent warmupDetection = iota
	warmupInvalid
	warmupWrongWorkspace
	warmupValid
)

// inspectWarmup decides whether msg is a genuine warmup message for this
// workspace and, if so, which warmup send it is a receipt for. It is the SINGLE
// point where inbound mail is recognised as warmup, and the only place the
// header-loss fallback may be reached.
//
// The primary signal is the X-Inroad-Warmup header: its HMAC token is verified
// against the warmup secret and the signed payload's workspace must equal the
// polled workspace. The header alone is never trusted.
//
// A message with NO header at all then gets the fallback (recoverWarmupSendID),
// because Microsoft strips unknown custom headers and warmup mail to an M365
// mailbox therefore arrives tokenless. That branch — and ONLY that branch — may
// fall back. warmupInvalid (a present but forged token) and warmupWrongWorkspace
// return their verdict immediately, for two reasons, NEITHER of which is access
// control:
//
//   - One message must produce ONE record of itself. A forged token is recorded as
//     invalid_token attack evidence, deliberately unattributed and
//     attribution_trusted=false. Letting the same message also mint a trusted
//     placement receipt would put two contradictory claims about it in the
//     evidence trail, and the trail is what an operator reads to tell an attack
//     from header loss.
//   - This function returns one verdict, so a message that took both branches
//     would need the caller to run both — and the junk path's caller, which
//     switches on the verdict to decide what to record, would then have to
//     re-derive which of the two happened.
//
// What it does NOT do is keep an attacker out of the fallback: the fallback is
// gated on token == "" and nothing else, so anyone who simply omits the header
// reaches it. That is intended, it is the only way header loss can be recovered
// at all, and the control on it is the lookup's own key + binding
// (recoverWarmupSendID), not the presence or absence of a token.
//
// The returned warmupSendID is the token payload's id, or the id of the resolved
// warmup_sends row — never a reconstructed token. A non-nil error means the
// fallback could not answer, NOT that the message is or is not warmup; callers
// apply their own path's error policy to it.
func inspectWarmup(ctx context.Context, core coreapi.Client, hook warmupHook, p queue.InboxPollPayload, msg mail.InboundMessage) (warmupSendID string, detection warmupDetection, fingerprint string, err error) {
	token := msg.Header.Get(warmup.HeaderWarmup)
	if token == "" {
		sendID, err := recoverWarmupSendID(ctx, core, p, msg)
		switch {
		case err != nil:
			return "", warmupAbsent, "", err
		case sendID != "":
			return sendID, warmupValid, "", nil
		default:
			return "", warmupAbsent, "", nil
		}
	}
	sum := sha256.Sum256([]byte(token))
	fingerprint = hex.EncodeToString(sum[:])
	payload, ok := warmup.Verify(token, hook.secret)
	if !ok {
		return "", warmupInvalid, fingerprint, nil
	}
	if payload.WorkspaceID != p.WorkspaceID {
		return "", warmupWrongWorkspace, fingerprint, nil
	}
	return payload.WarmupSendID, warmupValid, fingerprint, nil
}

// recoverWarmupSendID resolves a tokenless inbound message back to the warmup send
// it is a receipt for, by the Message-ID we recorded when we sent it
// (warmup_sends.message_id, written by MarkWarmupSent). It returns "" for
// "not a warmup message", which is the answer for nearly every message the poller
// sees.
//
// What is ours and what is not: the PREDICATE is our own data — the match requires
// a 'sent' warmup send this workspace made, addressed to THIS polled mailbox — but
// the KEY is not. messageID comes off unauthenticated inbound mail, exactly like
// the Original-Message-ID the warmup DSN path treats as attacker-controlled. So
// this widens detection to "whoever can present the Message-ID of a warmup send
// addressed to this mailbox", and does not narrow it further: the receipt INSERT
// re-proves the same facts this lookup already matched, so it is not a second
// check.
//
// That residual is accepted rather than overlooked, and it is sized by the id: a
// warmup send's Message-ID is CSPRNG-generated on the smtp and gmail paths
// (go-mail SetMessageID — 22 chars from crypto/rand, ~132 bits) and never leaves
// the workspace, so presenting one means having already observed the message. On
// m365 the id is whatever Exchange assigned, whose entropy is Microsoft's
// business — worth knowing, since m365 is the provider this fallback exists for.
// The exposure is one warmup placement sample for a mailbox the attacker can
// already mail, in a pool that is intra-workspace only (see invariant 62's
// adversary model, which invariant 52 rests on): worse than a token, better than
// the alternative of counting M365 placement as nothing at all.
//
// Both directions of a warmup exchange are the same shape — the A→B receipt and
// the B→A engage-reply are each a warmup_sends row whose to_mailbox is the mailbox
// now polling — so one lookup covers both. In-Reply-To / References are
// deliberately NOT walked: those carry OTHER messages' ids, so matching them would
// widen the key from "presented this message" to "presented anything that ever
// referenced it", for no gain.
//
// A core without the capability (an existing worker fake, a future HTTP coreapi
// that has not grown the endpoint) degrades to header-only detection, which is
// exactly today's behaviour — never a failed poll.
func recoverWarmupSendID(ctx context.Context, core coreapi.Client, p queue.InboxPollPayload, msg mail.InboundMessage) (string, error) {
	lookup, ok := core.(coreapi.WarmupSendLookupClient)
	if !ok {
		return "", nil
	}
	// An absent Message-ID cannot identify anything, and warmup_sends.message_id
	// DEFAULTs to '' — so a lookup on "" would ask the database to match every
	// queued row rather than nothing. An id carrying a byte Postgres cannot
	// encode gets the same "not warmup" answer rather than a permanent lookup
	// failure that would wedge this mailbox's cursor forever (usableMessageID).
	messageID := strings.TrimSpace(msg.Header.Get("Message-ID"))
	if !usableMessageID(messageID) {
		return "", nil
	}
	ref, found, err := lookup.FindWarmupSendByMessageID(ctx, p.WorkspaceID, p.MailboxID, messageID)
	if err != nil {
		return "", fmt.Errorf("warmup header-loss lookup: %w", err)
	}
	if !found {
		return "", nil
	}
	// Logged because the alternative failure mode is invisible: a fallback that has
	// stopped resolving anything looks exactly like a fleet with no header loss, and
	// the only symptom would be a placement sample that quietly shrinks again.
	slog.InfoContext(ctx, "inbox_poll_warmup_recovered_by_message_id",
		"workspace_id", p.WorkspaceID, "mailbox_id", p.MailboxID, "warmup_send_id", ref.WarmupSendID)
	return ref.WarmupSendID, nil
}

// processInbound is the warmup receipt-detection HOOK in front of campaign
// classification (spec §9.4 isolation). If inspectWarmup resolves msg to a warmup
// send in this workspace it is recorded + engaged via recordWarmup and STOPPED — it
// never reaches reply/bounce classification, so warmup mail can never stop,
// suppress, or bounce a campaign enrollment. It reports matched=false for warmup
// (it is neither a reply nor a bounce — counted as skipped in the poll summary).
// A non-warmup message falls through to processMessage unchanged. path describes
// which folder this pass was reading and what it could observe about a tab.
func processInbound(ctx context.Context, core coreapi.Client, classifier *replyclassify.Classifier, hook warmupHook, p queue.InboxPollPayload, msg mail.InboundMessage, path readingPath, replies, bounces *int) (bool, error) {
	warmupSendID, detection, fingerprint, err := inspectWarmup(ctx, core, hook, p, msg)
	if err != nil {
		// The fallback could not answer, so we do not KNOW whether this is warmup.
		// Failing the poll leaves the cursor where it is and the retry re-examines
		// the message — the same policy as a failed recordWarmup below, and for the
		// same reason: classifying a possible warmup message as campaign mail is a
		// decision we would never revisit.
		return false, err
	}
	if detection == warmupValid {
		if err := recordWarmup(ctx, core, hook, p, warmupSendID, msg, path); err != nil {
			return false, err
		}
		return false, nil
	}
	if detection == warmupInvalid || detection == warmupWrongWorkspace {
		reason := "invalid_signature"
		if detection == warmupWrongWorkspace {
			reason = "workspace_mismatch"
		}
		recordWarmupTokenFailure(ctx, core, p, fingerprint, reason)
	}
	return processMessage(ctx, core, classifier, p.WorkspaceID, p.MailboxID, msg, replies, bounces)
}

func recordWarmupTokenFailure(ctx context.Context, core coreapi.Client, p queue.InboxPollPayload, fingerprint, reason string) {
	evidence, ok := core.(coreapi.WarmupEvidenceClient)
	if !ok {
		return
	}
	if err := evidence.RecordWarmupTokenFailure(ctx, p.WorkspaceID, p.MailboxID, fingerprint, reason); err != nil {
		slog.Warn("inbox_poll_warmup_token_evidence_failed", "mailbox_id", p.MailboxID, "reason", reason, "err", err)
	}
}

// recordWarmup records a detected warmup message's receipt (idempotent, spec §7)
// and, on a genuinely new receipt (non-empty plan), enqueues its delayed
// engagement. RecipientMailbox is the polled mailbox (p.MailboxID) — it OBSERVED
// the message; warmupSendID is inspectWarmup's resolved send (the verified token's
// payload, or the warmup_sends row a tokenless message was matched to); MessageID
// is the received message's RFC822 Message-ID and sourceFolder the folder it was
// found in, both persisted so C5b's engager can relocate the exact message. A
// duplicate re-poll returns an empty plan (ReceiptID ""), so no engage is
// re-enqueued.
//
// It takes the send id rather than a warmup.Payload BECAUSE of the second source:
// a Payload is the signed body of a receipt token, and a path that could build one
// from a database row would be a path that could manufacture a token. The id is
// the only field this function ever used.
//
// The sending identity is extracted here too (design §6), from the headers of a
// message that has ALREADY been proven warmup — by HMAC token verification, or by
// matching a send we ourselves made to this mailbox — which is what makes reading
// headers acceptable at all in a path whose last two live findings were both forged
// inputs. warmup.ExtractIdentity is pure and cannot fail: it returns unknown
// verdicts and empty domains rather than an error, so nothing about the identity
// can stop the receipt or hold back the poll cursor.
func recordWarmup(ctx context.Context, core coreapi.Client, hook warmupHook, p queue.InboxPollPayload, warmupSendID string, msg mail.InboundMessage, path readingPath) error {
	// Whose verdicts these are is decided by hook.receiver: only an
	// Authentication-Results header stamped by THIS mailbox's own system is
	// believed, so the extractor is told which system that is. Everything below the
	// receiving boundary is sender-influenceable (design §3.1).
	identity := warmup.ExtractIdentity(msg.Header, hook.receiver)
	plan, err := core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID:      p.WorkspaceID,
		WarmupSendID:     warmupSendID,
		RecipientMailbox: p.MailboxID,
		Placement:        warmupPlacement(path, msg.PlacementCategory),
		SourceFolder:     path.sourceFolder,
		MessageID:        msg.Header.Get("Message-ID"),
		// The READER's capability, not the mailbox's. Recorded with the evidence so
		// it stays true of the row: a mailbox migrated between providers must not make
		// this observation claim a capability the reader never had.
		TabCapable: path.tabCapable,
		// Metadata on the observation, never a reason to refuse it (design §7/§8).
		DKIMDomain:       identity.DKIMDomain,
		ReturnPathDomain: identity.ReturnPathDomain,
		SPFResult:        identity.SPFResult,
		DKIMResult:       identity.DKIMResult,
		DMARCResult:      identity.DMARCResult,
		// The relay this message was observed arriving from, read from the same header
		// and the same Receiver the identity was, so "who received this" is decided
		// once. Empty when no hop could be attributed to the receiver.
		ObservedRelayIP: warmup.ObservedRelayIP(msg.Header, hook.receiver),
	})
	if err != nil {
		return err
	}
	if plan.ReceiptID == "" {
		return nil // duplicate receipt (re-poll) — already engaged/queued
	}
	return hook.enq.EnqueueWarmupEngageIn(ctx, plan.ReceiptID, p.WorkspaceID, plan.EngageAfter)
}

// scanIMAPJunk best-effort scans the IMAP junk folder for spam-placed warmup mail
// and records each verified receipt with placement "spam". It NEVER fails the
// poll: ErrNoJunkFolder (no resolvable junk folder) is logged at debug and any
// other fetch/record error at warn, then the poll continues — the scan is
// stateless + idempotent, so anything missed is retried next poll without holding
// back the INBOX cursor. Non-warmup junk mail is deliberately ignored (never
// classified): the junk scan exists only to observe warmup placement.
func scanIMAPJunk(ctx context.Context, core coreapi.Client, hook warmupHook, js imapJunkScanner, cfg mail.IMAPConfig, p queue.InboxPollPayload, tabCapable bool) {
	msgs, folder, err := js.FetchJunk(ctx, cfg, junkScanBatch)
	if err != nil {
		logJunkScanErr(err, p, "imap")
		return
	}
	scanJunkForWarmup(ctx, core, hook, p, msgs, junkPath(folder, tabCapable))
}

// scanAPIJunk is the API-provider (gmail SPAM / m365 JunkEmail) counterpart of
// scanIMAPJunk. Same isolation + no-fail policy. The provider-specific source
// folder label (SPAM vs JunkEmail) is recorded for the engager.
func scanAPIJunk(ctx context.Context, core coreapi.Client, hook warmupHook, junkScan apiJunkScan, accessToken, provider string, p queue.InboxPollPayload, tabCapable bool) {
	msgs, err := junkScan(ctx, accessToken, junkScanBatch)
	if err != nil {
		logJunkScanErr(err, p, provider)
		return
	}
	scanJunkForWarmup(ctx, core, hook, p, msgs, junkPath(apiJunkFolderLabel(provider), tabCapable))
}

// apiJunkFolderLabel is the source-folder label recorded for a spam-placed API
// message, so C5b's engager knows which provider folder to act on.
func apiJunkFolderLabel(provider string) string {
	if provider == "gmail" {
		return "SPAM"
	}
	return "JunkEmail"
}

// scanJunkForWarmup records a "spam" receipt for every VERIFIED warmup message in
// a junk batch. A record error stops this batch (returned via logJunkScanErr at
// the caller is not applicable here — errors are logged inline) but never the
// poll: the stateless rescan retries it. Non-warmup junk is skipped.
//
// Skipped INCLUDING feedback reports, deliberately. FBL reports are commonly
// spam-foldered, so a spam-placed ARF is a real complaint this pass walks past and
// does not ingest (recordInboundComplaint is reached only from processMessage, on
// the INBOX pass). That is a KNOWN GAP, left open on purpose: this scan exists to
// observe warmup placement and reads a folder chosen by a spam filter, and giving
// it the power to suppress a contact — which is what ingesting a complaint does —
// is a widening of unauthenticated-input handling that deserves its own design
// pass, not an incidental one inside a scan whose whole contract is "best-effort,
// never fails the poll". Whoever picks that up: the complaint path is
// authenticated by the resolved send, not by the folder, so the change is small —
// the question to answer first is what a spam-filter-chosen folder is allowed to
// trigger.
func scanJunkForWarmup(ctx context.Context, core coreapi.Client, hook warmupHook, p queue.InboxPollPayload, msgs []mail.InboundMessage, path readingPath) {
	for _, msg := range msgs {
		warmupSendID, detection, fingerprint, err := inspectWarmup(ctx, core, hook, p, msg)
		if err != nil {
			// Junk-path errors never fail the poll (see this function's contract):
			// the scan is stateless and idempotent, so the next poll asks again.
			slog.Warn("inbox_poll_junk_warmup_lookup_failed", "mailbox_id", p.MailboxID, "err", err)
			continue
		}
		if detection != warmupValid {
			switch detection {
			case warmupInvalid:
				recordWarmupTokenFailure(ctx, core, p, fingerprint, "invalid_signature")
			case warmupWrongWorkspace:
				recordWarmupTokenFailure(ctx, core, p, fingerprint, "workspace_mismatch")
			}
			continue // non-warmup junk is ignored — never classified
		}
		if err := recordWarmup(ctx, core, hook, p, warmupSendID, msg, path); err != nil {
			slog.Warn("inbox_poll_junk_warmup_record_failed", "mailbox_id", p.MailboxID, "err", err)
			// keep scanning the rest of the batch; the failed one is idempotently
			// retried on the next poll's rescan.
		}
	}
}

// logJunkScanErr classifies a junk-scan fetch failure: a missing junk folder is
// an expected, benign outcome (debug); any other error is a transient scan
// failure (warn). Neither fails the poll.
func logJunkScanErr(err error, p queue.InboxPollPayload, provider string) {
	if errors.Is(err, mail.ErrNoJunkFolder) {
		slog.Debug("inbox_poll_no_junk_folder", "mailbox_id", p.MailboxID, "provider", provider)
		return
	}
	slog.Warn("inbox_poll_junk_scan_failed", "mailbox_id", p.MailboxID, "provider", provider, "err", err)
}

// logUnresolvedBounce records a permanent bounce that could not be attributed to
// a send. This is a no-op by design (see processMessage), but never a silent
// one: without a trace, a DSN parser that has stopped resolving anything looks
// exactly like a workspace that simply has no bounces, and the only symptom
// would be reputation decay weeks later.
//
// WARN, not ERROR: a handful of these is normal (forwarded reports, mail sent
// before this workspace existed). It is the RATE that is diagnostic, so it is
// logged at a level an operator can alert on without paging on the expected few.
//
// reason is a stable token, not a sentence, so it can be grouped. The failed
// recipient is deliberately NOT logged — it is an unauthenticated,
// attacker-supplied address off an unverified message, and the repo's rule is
// ids and reason tokens over message content.
func logUnresolvedBounce(ctx context.Context, workspaceID, mailboxID string, d DSNResult, reason string) {
	slog.WarnContext(ctx, "inbox_poll_bounce_unresolved",
		"workspace_id", workspaceID, "mailbox_id", mailboxID,
		"reason", reason, "status", d.StatusCode,
		"original_message_id", d.OriginalMessageID)
}

// recordInboundComplaint records a parsed feedback report as a complaint on the
// EXISTING ingest — the same idempotent write POST /deliverability/events feeds,
// so the suppression, the deliverability score, the at-risk list and the campaign
// circuit breaker all consume it with no changes and no second path.
//
// What the report is allowed to establish is deliberately narrow. An ARF arrives
// as ordinary, unauthenticated mail: anyone who can email a connected mailbox can
// deliver one, and an ingested complaint suppresses an address workspace-wide and
// can pause a campaign (docs/security.md invariants 40, 42). So the report's own
// Original-Rcpt-To is never the thing acted on. The Message-ID it QUOTES is
// resolved against our own sends, and the complaint is recorded against that
// send's contact — the same discipline the hard-bounce arm above uses, and for the
// same reason it refuses to suppress on Final-Recipient. The forgery surface
// shrinks to "already knows a real Message-ID of a real send", which is the
// documented residual bar for reply-driven suppression rather than a new one.
//
// It returns matched=true only for a complaint actually ingested. Every refusal is
// a logged skip, never an error: a report we cannot attribute must not hold back
// the cursor that campaign replies and bounces also ride on. An ingest FAILURE, by
// contrast, does propagate — the write is idempotent on provider_event_id, so a
// retry cannot double-count, and dropping a complaint is a compliance failure.
func recordInboundComplaint(ctx context.Context, core coreapi.Client, workspaceID, mailboxID string, a ARFResult) (bool, error) {
	ingest, ok := core.(coreapi.DeliverabilityComplaintClient)
	if !ok {
		// No capability: the report is parsed and dropped, which is the behaviour
		// before this path existed. Logged so a deployment that quietly cannot
		// record complaints is visible.
		logDeclinedComplaint(ctx, workspaceID, mailboxID, a, "capability_unavailable")
		return false, nil
	}
	// A redacted original message leaves nothing but an attacker-supplied address,
	// which is precisely what must not be acted on alone. An id we cannot use as
	// a lookup key is declined for the same reason and one more: asking Postgres
	// about it fails permanently and would freeze this mailbox's cursor
	// (usableMessageID), which is exactly the outcome the ErrInvalidComplaint arm
	// below exists to prevent.
	originalMessageID := strings.TrimSpace(a.OriginalMessageID)
	if !usableMessageID(originalMessageID) {
		reason := "no_original_message_id"
		if originalMessageID != "" {
			reason = "unusable_original_message_id"
		}
		logDeclinedComplaint(ctx, workspaceID, mailboxID, a, reason)
		return false, nil
	}
	// A warmup send can never be resolved here: warmup mail lives in warmup_sends,
	// which this lookup does not read, so a report quoting one falls out as
	// no_matching_send rather than becoming a campaign complaint (spec §9.4).
	s, err := core.FindSendByMessageID(ctx, workspaceID, originalMessageID)
	if err != nil {
		if errors.Is(err, coreapi.ErrNoMatch) {
			// A forwarded report, a purged send, or a forgery quoting an id we do
			// not have. Logged rather than dropped silently, so a parser that has
			// stopped resolving anything is a rising count and not a reputation
			// problem discovered weeks later.
			logDeclinedComplaint(ctx, workspaceID, mailboxID, a, "no_matching_send")
			return false, nil
		}
		return false, err
	}
	// Cross-check, one direction only: the send row is the authority, and a report
	// that names a DIFFERENT recipient than the send went to is refused rather than
	// redirected. Case-insensitive, because the same mailbox in a different case is
	// the same mailbox and a real complaint must not be thrown away over it.
	if a.ComplainedRecipient != "" && !strings.EqualFold(a.ComplainedRecipient, s.ContactEmail) {
		logDeclinedComplaint(ctx, workspaceID, mailboxID, a, "recipient_mismatch")
		return false, nil
	}
	if err := ingest.IngestComplaint(ctx, coreapi.ComplaintInput{
		WorkspaceID: workspaceID,
		// OUR send's contact, never the reported address.
		Email: s.ContactEmail,
		// One complaint per send, forever: a re-poll or a redelivered report is a
		// no-op. Prefixed so a mail-borne report cannot collide with a provider
		// feed's own event ids.
		ProviderEventID: "arf:" + s.SendID,
		SendID:          s.SendID,
	}); err != nil {
		// A PERMANENT rejection is skipped, not retried. Retrying one does not merely
		// waste a task: this function returns before SetInboxCursor, so the mailbox's
		// cursor never advances and EVERY inbound signal for it stops — campaign
		// replies, bounces, warmup receipts — indefinitely, on the strength of one
		// unauthenticated inbound message. Everything else is transient and does
		// retry, because the ingest is idempotent and losing a complaint is a
		// compliance failure.
		if errors.Is(err, coreapi.ErrInvalidComplaint) {
			logDeclinedComplaint(ctx, workspaceID, mailboxID, a, "ingest_rejected")
			return false, nil
		}
		return false, fmt.Errorf("ingest inbound complaint: %w", err)
	}
	slog.InfoContext(ctx, "inbox_poll_complaint_ingested",
		"workspace_id", workspaceID, "mailbox_id", mailboxID,
		"send_id", s.SendID, "feedback_type", a.FeedbackType)
	return true, nil
}

// logDeclinedComplaint records a feedback report that was NOT ingested, with a
// stable reason token so the reasons can be grouped and alerted on by RATE — a
// handful is normal, a step change is a bug.
//
// The complained-about address is deliberately NOT logged: it is an
// attacker-supplied address off an unauthenticated message, and this repo's rule
// is ids and reason tokens over message content. The feedback type is safe (a
// small closed vocabulary) and is what distinguishes a declined complaint from a
// report we never intended to act on.
func logDeclinedComplaint(ctx context.Context, workspaceID, mailboxID string, a ARFResult, reason string) {
	slog.WarnContext(ctx, "inbox_poll_complaint_declined",
		"workspace_id", workspaceID, "mailbox_id", mailboxID,
		"reason", reason, "feedback_type", a.FeedbackType,
		"original_message_id", a.OriginalMessageID)
}

// processMessage classifies one fetched message and takes the corresponding
// action. A DSN is handled first (hard bounce → MarkBounced) and never falls
// through to the reply path, then an RFC 5965 feedback report (→ a complaint on
// the existing ingest) likewise. A non-DSN message that matches a send is
// classified, stored in the unified inbox, and then dispatched on the
// WORKSPACE'S REPLY LABEL for that class rather than on the class itself:
// suppresses_contact → MarkUnsubscribed, stops_enrollment → MarkReplied,
// captures_deal → CRM capture, defers_enrollment → a return-date deferral. See
// replyDispatch.byLabel; a class no label claims falls back to byClass, the
// pre-taxonomy switch.
//
// The seeded builtin labels carry exactly the flags that reproduce byClass, so
// a workspace that has not touched its taxonomy behaves identically either way.
//
// *bounces is bumped on a marked hard bounce; *replies is bumped on a matched
// reply routed to MarkReplied that actually has an enrollment to stop (so the
// metric keeps its "engaged enrollment reply" meaning). enrollmentID may be ""
// (legacy direct-send): classification and routing still run — MarkUnsubscribed
// still suppresses the address, and the tagged RecordReplyClass/MarkReplied
// writes no-op the enrollment update coreapi-side. The returned bool reports
// whether the message matched (a bounce or a stopping/suppressing reply) — used
// only for the skipped-count in the poll summary log; an automated tag reports
// false so it counts as skipped rather than a reply.
func processMessage(ctx context.Context, core coreapi.Client, classifier *replyclassify.Classifier, workspaceID, mailboxID string, msg mail.InboundMessage, replies, bounces *int) (bool, error) {
	d := ParseDSN(msg.Header, msg.ContentType, msg.Body)
	if d.Kind != NotABounce {
		// A DSN is never also a reply — always handled here, never falls
		// through to the reply-matching path below.
		switch d.Kind {
		case HardBounce:
			// No usable original Message-ID: the bounce is real but
			// unattributable. Final-Recipient names an address, but a DSN is
			// unauthenticated and its recipient is attacker-supplied, so
			// suppressing on it would let anyone kill an address they don't own
			// with a forged report. Log and skip instead of guessing.
			//
			// Checked BEFORE both lookups, not between them: the id is a Postgres
			// text parameter on either path, so a byte Postgres cannot encode
			// fails the warmup lookup just as permanently as the campaign one and
			// would wedge this mailbox's cursor forever (usableMessageID). An
			// EMPTY id short-circuits identically to before — RecordWarmupHardBounce
			// already refuses one — so only the unusable case changes.
			originalMessageID := strings.TrimSpace(d.OriginalMessageID)
			if !usableMessageID(originalMessageID) {
				reason := "no_message_id"
				if originalMessageID != "" {
					reason = "unusable_message_id"
				}
				logUnresolvedBounce(ctx, workspaceID, mailboxID, d, reason)
				return false, nil
			}
			if evidence, ok := core.(coreapi.WarmupEvidenceClient); ok {
				matched, err := evidence.RecordWarmupHardBounce(ctx, workspaceID, originalMessageID, mailboxID)
				if err != nil {
					return false, err
				}
				if matched {
					*bounces++
					return true, nil
				}
			}
			s, err := core.FindSendByMessageID(ctx, workspaceID, originalMessageID)
			if err != nil {
				if errors.Is(err, coreapi.ErrNoMatch) {
					// Best-effort by contract: a bounce for mail this workspace
					// never sent (forwarded DSN, purged send) must not fail the
					// poll. Logged rather than dropped, so a parser that has
					// silently stopped resolving anything is visible as a rising
					// count instead of a slowly rotting recipient list.
					logUnresolvedBounce(ctx, workspaceID, mailboxID, d, "no_matching_send")
					return false, nil
				}
				return false, err
			}
			if err := core.MarkBounced(ctx, s.EnrollmentID, workspaceID, s.ContactEmail, true); err != nil {
				return false, err
			}
			*bounces++
			return true, nil
		default:
			// SoftBounce (4.x.x): TRANSIENT. Log only — never suppress. A full
			// mailbox, a greylisting deferral or a temporary DNS failure is
			// recoverable, and suppression is not: an address killed on a 4.x.x
			// is a deliverable contact silently removed from every future
			// campaign, with no operator-visible cause. The sending MTA retries
			// these on its own; if the failure is really permanent it will
			// eventually arrive as a 5.x.x and be suppressed then.
			slog.InfoContext(ctx, "inbox_poll_soft_bounce",
				"workspace_id", workspaceID, "mailbox_id", mailboxID,
				"status", d.StatusCode, "original_message_id", d.OriginalMessageID)
			return true, nil
		}
	}

	// The OTHER multipart/report ParseDSN declines: an RFC 5965 abuse feedback
	// report. Handled here, after the DSN branch and before reply matching, for the
	// same reason a DSN is — a report about a message is never itself a reply to it.
	if a := ParseARF(msg.Header, msg.ContentType, msg.Body); a.Kind == AbuseComplaint {
		return recordInboundComplaint(ctx, core, workspaceID, mailboxID, a)
	}

	// The standalone IsAutoReply early-skip is intentionally gone: the
	// classifier's Layer 1 is a strict superset of it, so an automated reply is
	// now routed through classification (tagged via RecordReplyClass) rather
	// than silently dropped before it ever matches a send.
	for _, id := range MessageIDs(msg.Header) {
		s, err := core.FindSendByMessageID(ctx, workspaceID, id)
		if err != nil {
			if errors.Is(err, coreapi.ErrNoMatch) {
				continue
			}
			return false, err
		}

		in := replyclassify.Input{
			Headers:  map[string][]string(msg.Header), // net/mail.Header is already map[string][]string
			Subject:  msg.Header.Get("Subject"),
			BodyText: string(msg.Body),
		}
		r := classifier.Classify(ctx, in)

		// Store the matched reply in the unified inbox EXACTLY ONCE, before the
		// class-based switch below, so every reachable class — automated
		// (auto_reply/out_of_office), unsubscribe, and positive/negative/
		// neutral/unknown — is captured, not just the ones that stop an
		// enrollment. InboxCaptureClient is an OPTIONAL execution-plane
		// capability (feature-detected via type assertion, like
		// coreapi.CRMCaptureClient below): a core that doesn't implement it
		// (e.g. a worker fake) simply skips storage without panicking.
		if capture, ok := core.(coreapi.InboxCaptureClient); ok {
			plainText, html, _ := mime.Extract(msg.ContentType, msg.Body) // Extract never errors per its own contract
			from, fromName := msg.Header.Get("From"), ""
			if addr, parseErr := netmail.ParseAddress(msg.Header.Get("From")); parseErr == nil {
				from, fromName = addr.Address, addr.Name
			}
			occurredAt := time.Now().UTC()
			if headerDate, dateErr := msg.Header.Date(); dateErr == nil {
				occurredAt = headerDate.UTC()
			}
			// campaignID/contactID are always populated: sends.campaign_id and
			// sends.contact_id are NOT NULL columns, so every matched send
			// carries both regardless of whether it belongs to an enrollment.
			campaignID, contactID := s.CampaignID, s.ContactID
			if err := capture.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
				WorkspaceID: workspaceID, MailboxID: s.MailboxID, CampaignID: &campaignID, ContactID: &contactID,
				// RootMessageID anchors the thread on the send's OWN outbound
				// Message-ID (s.MessageID) — the value this reply's
				// In-Reply-To/References matched against — never this reply's
				// own Message-ID.
				RootMessageID: s.MessageID, Subject: msg.Header.Get("Subject"),
				MessageID: strings.TrimSpace(msg.Header.Get("Message-ID")),
				FromEmail: strings.ToLower(from), FromName: fromName, ToEmail: strings.ToLower(msg.Header.Get("To")),
				BodyText: plainText, BodyHTML: html, ReplyClass: r.Class, OccurredAt: occurredAt,
			}); err != nil {
				return false, err
			}
		}

		// What HAPPENS to the enrollment/contact/deal is read off the workspace's
		// reply label for this class (its role flags), not compiled into a
		// switch. A key no label claims falls back to the pre-taxonomy switch —
		// see replyDispatch.byClass.
		d := replyDispatch{
			core: core, classifier: classifier, workspaceID: workspaceID,
			msg: msg, in: in, send: s, result: r, replies: replies,
		}
		if label, ok := resolveReplyLabel(ctx, core, workspaceID, r.Class); ok {
			return d.byLabel(ctx, label)
		}
		return d.byClass(ctx)
	}
	return false, nil
}

// zeroize overwrites the decrypted IMAP password in place after use. Mirrors
// sequence.zeroize.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
