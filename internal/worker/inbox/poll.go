package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	EnqueueWarmupEngageIn(receiptID, workspaceID string, d time.Duration) error
}

// imapJunkScanner is the OPTIONAL junk-folder capability of an IMAP InboxReader:
// a best-effort scan of the mailbox's spam/junk folder for warmup mail (spec §7).
// *mail.NetInboxReader implements it; a reader that does not (e.g. a test fake
// that only cares about INBOX) is simply scanned INBOX-only. Kept separate from
// mail.InboxReader (interface segregation) so the core poll path never depends on
// junk support.
type imapJunkScanner interface {
	FetchJunk(cfg mail.IMAPConfig, maxN int) (msgs []mail.InboundMessage, folder string, err error)
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
// match the warmup_receipts.placement CHECK constraint.
const (
	placementInbox = "inbox"
	placementSpam  = "spam"
)

// sourceFolderInbox is the canonical source-folder label for a message found in
// the primary inbox across all providers (IMAP "INBOX", the Gmail INBOX label,
// the Graph Inbox folder), so C5b's engager can act on it uniformly.
const sourceFolderInbox = "INBOX"

// warmupHook carries the warmup receipt-detection dependencies threaded through
// the poll path: the HMAC secret that verifies the X-Inroad-Warmup token and the
// enqueuer for the delayed warmup:engage follow-up. It is the seam that keeps
// warmup mail ISOLATED from campaign reply/bounce classification (spec §9.4).
type warmupHook struct {
	secret []byte
	enq    WarmupEngageEnqueuer
}

// PollHandler returns an asynq handler for inbox:poll tasks. It dispatches on
// the mailbox provider: gmail polls via the Gmail API (opaque historyId cursor),
// m365 polls via the Microsoft Graph delta query (opaque delta-link cursor),
// smtp opens the mailbox's IMAP connection, establishes/validates the poll
// baseline via CurrentState, and fetches anything new since the stored UID
// cursor.
//
// Before campaign classification, every inbound message runs the warmup
// receipt-detection HOOK (processInbound): a verified X-Inroad-Warmup message for
// this workspace is recorded + engaged + STOPPED, never reaching reply/bounce
// classification (spec §9.4 isolation), so warmup traffic can never stop,
// suppress, or bounce a real campaign enrollment. Everything else falls through
// to the SAME reply/bounce classification (processMessage) unchanged. Each path
// also best-effort scans the provider's spam/junk folder for spam-placed warmup
// mail (the core deliverability health signal) and persists its cursor.
func PollHandler(core coreapi.Client, reader mail.InboxReader, gmail GmailFetcher, graph GraphFetcher, classifier *replyclassify.Classifier, warmupSecret []byte, enq WarmupEngageEnqueuer) func(context.Context, *asynq.Task) error {
	hook := warmupHook{secret: warmupSecret, enq: enq}
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPollPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		job, err := core.GetInboxPollJob(ctx, p.MailboxID, p.WorkspaceID)
		if err != nil {
			return err
		}

		if job.Provider == "gmail" {
			return pollAPI(ctx, core, gmail, classifier, hook, p, job, "gmail", gmailJunkScan(gmail))
		}
		if job.Provider == "m365" {
			return pollAPI(ctx, core, graph, classifier, hook, p, job, "m365", graphJunkScan(graph))
		}
		defer zeroize(job.Password)

		cfg := mail.IMAPConfig{Host: job.Host, Port: job.Port, Username: job.Username, Password: string(job.Password)}

		uidValidity, uidNext, err := reader.CurrentState(cfg)
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

		msgs, _, err := reader.Fetch(cfg, job.LastSeenUID, fetchBatchSize)
		if err != nil {
			return err
		}

		var replies, bounces, skipped int
		for _, msg := range msgs {
			matched, err := processInbound(ctx, core, classifier, hook, p, msg, placementInbox, sourceFolderInbox, &replies, &bounces)
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
			scanIMAPJunk(ctx, core, hook, js, cfg, p)
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
	for _, msg := range msgs {
		matched, err := processInbound(ctx, core, classifier, hook, p, msg, placementInbox, sourceFolderInbox, &replies, &bounces)
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
		scanAPIJunk(ctx, core, hook, junkScan, string(job.AccessToken), provider, p)
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

// detectWarmup reports whether msg is a genuine warmup message for the polled
// mailbox's workspace. It reads the X-Inroad-Warmup header, verifies its HMAC
// token against the warmup secret, and requires the signed payload's workspace to
// equal workspaceID. An absent / unsigned / forged / wrong-workspace header
// yields ok=false — NOT warmup — so the caller falls through to normal
// reply/bounce classification UNCHANGED (spec §9.3). The header alone is never
// trusted: the token is HMAC-verified before the message is treated as warmup.
type warmupDetection uint8

const (
	warmupAbsent warmupDetection = iota
	warmupInvalid
	warmupWrongWorkspace
	warmupValid
)

func inspectWarmup(msg mail.InboundMessage, secret []byte, workspaceID string) (warmup.Payload, warmupDetection, string) {
	token := msg.Header.Get(warmup.HeaderWarmup)
	if token == "" {
		return warmup.Payload{}, warmupAbsent, ""
	}
	sum := sha256.Sum256([]byte(token))
	fingerprint := hex.EncodeToString(sum[:])
	payload, ok := warmup.Verify(token, secret)
	if !ok {
		return warmup.Payload{}, warmupInvalid, fingerprint
	}
	if payload.WorkspaceID != workspaceID {
		return warmup.Payload{}, warmupWrongWorkspace, fingerprint
	}
	return payload, warmupValid, fingerprint
}

func detectWarmup(msg mail.InboundMessage, secret []byte, workspaceID string) (warmup.Payload, bool) {
	payload, detection, _ := inspectWarmup(msg, secret, workspaceID)
	return payload, detection == warmupValid
}

// processInbound is the warmup receipt-detection HOOK in front of campaign
// classification (spec §9.4 isolation). If msg is a verified warmup message for
// this workspace it is recorded + engaged via recordWarmup and then STOPPED — it
// never reaches reply/bounce classification, so warmup mail can never stop,
// suppress, or bounce a campaign enrollment. It reports matched=false for warmup
// (it is neither a reply nor a bounce — counted as skipped in the poll summary).
// A non-warmup message falls through to processMessage unchanged. placement is
// "inbox" here (INBOX pass); sourceFolder is the provider inbox label.
func processInbound(ctx context.Context, core coreapi.Client, classifier *replyclassify.Classifier, hook warmupHook, p queue.InboxPollPayload, msg mail.InboundMessage, placement, sourceFolder string, replies, bounces *int) (bool, error) {
	payload, detection, fingerprint := inspectWarmup(msg, hook.secret, p.WorkspaceID)
	if detection == warmupValid {
		if err := recordWarmup(ctx, core, hook, p, payload, msg, placement, sourceFolder); err != nil {
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
	return processMessage(ctx, core, classifier, p.WorkspaceID, msg, replies, bounces)
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
// the message; WarmupSendID comes from the verified token payload; MessageID is
// the received message's RFC822 Message-ID and sourceFolder the folder it was
// found in, both persisted so C5b's engager can relocate the exact message. A
// duplicate re-poll returns an empty plan (ReceiptID ""), so no engage is
// re-enqueued.
func recordWarmup(ctx context.Context, core coreapi.Client, hook warmupHook, p queue.InboxPollPayload, payload warmup.Payload, msg mail.InboundMessage, placement, sourceFolder string) error {
	plan, err := core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID:      p.WorkspaceID,
		WarmupSendID:     payload.WarmupSendID,
		RecipientMailbox: p.MailboxID,
		Placement:        placement,
		SourceFolder:     sourceFolder,
		MessageID:        msg.Header.Get("Message-ID"),
	})
	if err != nil {
		return err
	}
	if plan.ReceiptID == "" {
		return nil // duplicate receipt (re-poll) — already engaged/queued
	}
	return hook.enq.EnqueueWarmupEngageIn(plan.ReceiptID, p.WorkspaceID, plan.EngageAfter)
}

// scanIMAPJunk best-effort scans the IMAP junk folder for spam-placed warmup mail
// and records each verified receipt with placement "spam". It NEVER fails the
// poll: ErrNoJunkFolder (no resolvable junk folder) is logged at debug and any
// other fetch/record error at warn, then the poll continues — the scan is
// stateless + idempotent, so anything missed is retried next poll without holding
// back the INBOX cursor. Non-warmup junk mail is deliberately ignored (never
// classified): the junk scan exists only to observe warmup placement.
func scanIMAPJunk(ctx context.Context, core coreapi.Client, hook warmupHook, js imapJunkScanner, cfg mail.IMAPConfig, p queue.InboxPollPayload) {
	msgs, folder, err := js.FetchJunk(cfg, junkScanBatch)
	if err != nil {
		logJunkScanErr(err, p, "imap")
		return
	}
	scanJunkForWarmup(ctx, core, hook, p, msgs, folder)
}

// scanAPIJunk is the API-provider (gmail SPAM / m365 JunkEmail) counterpart of
// scanIMAPJunk. Same isolation + no-fail policy. The provider-specific source
// folder label (SPAM vs JunkEmail) is recorded for the engager.
func scanAPIJunk(ctx context.Context, core coreapi.Client, hook warmupHook, junkScan apiJunkScan, accessToken, provider string, p queue.InboxPollPayload) {
	msgs, err := junkScan(ctx, accessToken, junkScanBatch)
	if err != nil {
		logJunkScanErr(err, p, provider)
		return
	}
	scanJunkForWarmup(ctx, core, hook, p, msgs, apiJunkFolderLabel(provider))
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
func scanJunkForWarmup(ctx context.Context, core coreapi.Client, hook warmupHook, p queue.InboxPollPayload, msgs []mail.InboundMessage, folder string) {
	for _, msg := range msgs {
		payload, detection, fingerprint := inspectWarmup(msg, hook.secret, p.WorkspaceID)
		if detection != warmupValid {
			if detection == warmupInvalid {
				recordWarmupTokenFailure(ctx, core, p, fingerprint, "invalid_signature")
			} else if detection == warmupWrongWorkspace {
				recordWarmupTokenFailure(ctx, core, p, fingerprint, "workspace_mismatch")
			}
			continue // non-warmup junk is ignored — never classified
		}
		if err := recordWarmup(ctx, core, hook, p, payload, msg, placementSpam, folder); err != nil {
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

// processMessage classifies one fetched message and takes the corresponding
// action. A DSN is handled first (hard bounce → MarkBounced) and never falls
// through to the reply path. A non-DSN message that matches a send is
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
func processMessage(ctx context.Context, core coreapi.Client, classifier *replyclassify.Classifier, workspaceID string, msg mail.InboundMessage, replies, bounces *int) (bool, error) {
	d := ParseDSN(msg.Header, msg.ContentType, msg.Body)
	if d.Kind != NotABounce {
		// A DSN is never also a reply — always handled here, never falls
		// through to the reply-matching path below.
		switch d.Kind {
		case HardBounce:
			if evidence, ok := core.(coreapi.WarmupEvidenceClient); ok {
				matched, err := evidence.RecordWarmupHardBounce(ctx, workspaceID, d.OriginalMessageID)
				if err != nil {
					return false, err
				}
				if matched {
					*bounces++
					return true, nil
				}
			}
			s, err := core.FindSendByMessageID(ctx, workspaceID, d.OriginalMessageID)
			if err != nil {
				if errors.Is(err, coreapi.ErrNoMatch) {
					return false, nil
				}
				return false, err
			}
			if err := core.MarkBounced(ctx, s.EnrollmentID, workspaceID, s.ContactEmail, true); err != nil {
				return false, err
			}
			*bounces++
			return true, nil
		default: // SoftBounce: log only, no state change.
			slog.Info("inbox_poll_soft_bounce", "workspace_id", workspaceID, "status", d.StatusCode)
			return true, nil
		}
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
