//go:build integration

package inbox

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// arfFor builds an RFC 5965 feedback report about one specific send: the
// Message-ID it quotes and the recipient it names both have to come from the
// seeded fixture, because the poller resolves the complaint against our own send
// row and refuses a report that disagrees with it.
func arfFor(originalMessageID, recipient string) string {
	return "From: FBL Sender <fbl@fbl.provider.example>\n" +
		"To: abuse-reports@acme.test\n" +
		"Subject: FW: Spam complaint\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/report; report-type=feedback-report; boundary=\"ARFIT\"\n" +
		"\n" +
		"--ARFIT\n" +
		"Content-Type: text/plain; charset=\"US-ASCII\"\n" +
		"\n" +
		"This is an email abuse report.\n" +
		"\n" +
		"--ARFIT\n" +
		"Content-Type: message/feedback-report\n" +
		"\n" +
		"Feedback-Type: abuse\n" +
		"User-Agent: SomeFBL/1.0\n" +
		"Version: 1\n" +
		"Original-Mail-From: <bounces@acme.test>\n" +
		"Original-Rcpt-To: <" + recipient + ">\n" +
		"Reported-Domain: acme.test\n" +
		"\n" +
		"--ARFIT\n" +
		"Content-Type: message/rfc822-headers\n" +
		"\n" +
		"Message-ID: " + originalMessageID + "\n" +
		"From: from@acme.test\n" +
		"To: " + recipient + "\n" +
		"Subject: Hi\n" +
		"\n" +
		"--ARFIT--\n"
}

// complaintEvents returns every deliverability_events complaint row for the
// workspace as "provider_event_id email" — the evidence that the poller reused the
// ingest POST /deliverability/events feeds rather than writing its own.
func complaintEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT provider_event_id || ' ' || email FROM deliverability_events
		 WHERE workspace_id = $1 AND kind = 'complaint' ORDER BY provider_event_id`, ws)
	if err != nil {
		t.Fatalf("select complaint events: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan complaint event: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate complaint events: %v", err)
	}
	return out
}

// The end-to-end proof that an ARF arriving AS MAIL lands on the existing
// complaint path: the contact is suppressed through the same workspace-scoped
// suppression table a bounce and an unsubscribe use, and one deliverability_events
// row is written under the "arf:<send id>" idempotency key — so the score, the
// at-risk list and the campaign breaker all see it with no new plumbing.
//
// The second poll re-delivers the SAME report (a re-poll, or a provider that sends
// twice) and must change nothing: idempotent on provider_event_id means no second
// suppression and no second evaluation.
func TestInboxIntegrationARFComplaintSuppressesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	msgID := "<arf-orig-" + uuid.NewString() + "@acme.test>"
	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, msgID)
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	report := inboundMsg(t, 11, arfFor(msgID, fx.email))
	poll := PollHandler(fx.core, &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{report}},
		nil, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})
	task := pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())

	if err := poll(ctx, task); err != nil {
		t.Fatalf("poll: %v", err)
	}

	suppressed, err := q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: fx.ws, Lower: fx.email})
	if err != nil {
		t.Fatalf("is suppressed: %v", err)
	}
	if !suppressed {
		t.Fatalf("expected %s to be suppressed after a complaint", fx.email)
	}
	events := complaintEvents(t, ctx, pool, fx.ws)
	if len(events) != 1 {
		t.Fatalf("complaint events = %v, want exactly 1", events)
	}
	if !strings.HasPrefix(events[0], "arf:") {
		t.Errorf("provider_event_id %q is not namespaced with the arf: prefix", events[0])
	}
	if !strings.HasSuffix(events[0], " "+fx.email) {
		t.Errorf("complaint event %q is not against the send's own contact (%s)", events[0], fx.email)
	}

	if err := poll(ctx, task); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if again := complaintEvents(t, ctx, pool, fx.ws); len(again) != 1 {
		t.Fatalf("complaint events after a re-poll = %v, want still exactly 1 "+
			"(idempotent on provider_event_id)", again)
	}
}

// A report quoting a Message-ID this workspace never sent resolves to nothing and
// suppresses nobody — the guard that keeps an unauthenticated inbound report from
// killing an address its sender does not own.
func TestInboxIntegrationARFForAnUnknownSendSuppressesNothing(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, "<step1-"+uuid.NewString()+"@acme.test>")
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	// A real contact address, but quoting a message we never sent.
	report := inboundMsg(t, 11, arfFor("<never-sent@elsewhere.example>", fx.email))
	poll := PollHandler(fx.core, &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{report}},
		nil, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})
	if err := poll(ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err != nil {
		t.Fatalf("an unattributable report must not fail the poll: %v", err)
	}

	suppressed, err := q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: fx.ws, Lower: fx.email})
	if err != nil {
		t.Fatalf("is suppressed: %v", err)
	}
	if suppressed {
		t.Fatalf("%s was suppressed by a report naming a send we never made", fx.email)
	}
	if events := complaintEvents(t, ctx, pool, fx.ws); len(events) != 0 {
		t.Fatalf("complaint events = %v, want none", events)
	}
}
