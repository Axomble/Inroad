//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// GetInboxPollJob must carry the polled mailbox's OWN address on every provider
// branch, because that is the only thing that lets the identity extractor decide
// which Authentication-Results header speaks for the receiving system (design §6).
//
// The API branch is the one this slice had to fix: it returned Provider,
// AccessToken and Cursor and no identity at all. Its failure mode is silent — the
// verdicts simply come back unknown, which is indistinguishable from a provider
// that stamps nothing — and gmail and m365 are precisely the two providers that DO
// stamp results, so the bug would have made the entire signal unobservable while
// looking like an honest absence.
//
// ImapUsername is asserted empty on that branch on purpose: it is the field a
// reader might reach for instead, and it holds nothing here.
func TestInboxPollJobCarriesTheMailboxAddressOnEveryProviderBranch(t *testing.T) {
	ctx := context.Background()
	pool := pollJobPool(t, ctx)
	q := gen.New(pool)
	sealer, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, "Poll identity "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	// A configured Google client so the gmail branch resolves a config rather than
	// failing "oauth not configured". No network is reached: the sealed token is
	// unexpired, and oauth2's ReuseTokenSource only refreshes an expired one.
	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test",
		mail.GoogleOAuth{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://app.test/cb"},
		mail.MicrosoftOAuth{}, []byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())

	imapID := pollJobMailbox(t, ctx, q, ws.ID, "smtp", "imap-box@recv.test",
		sealMailboxSecret(t, sealer, []byte("smtp-app-password")))
	gmailID := pollJobMailbox(t, ctx, q, ws.ID, "gmail", "gmail-box@recv.test",
		sealMailboxSecret(t, sealer, marshalUnexpiredToken(t)))

	for _, tc := range []struct {
		name         string
		mailbox      uuid.UUID
		wantProvider string
		wantEmail    string
	}{
		{"imap", imapID, "smtp", "imap-box@recv.test"},
		{"gmail", gmailID, "gmail", "gmail-box@recv.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, err := core.GetInboxPollJob(ctx, tc.mailbox.String(), ws.ID.String())
			if err != nil {
				t.Fatalf("GetInboxPollJob: %v", err)
			}
			if job.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", job.Provider, tc.wantProvider)
			}
			if job.Email != tc.wantEmail {
				t.Errorf("Email = %q, want %q: without the receiver's own address the identity "+
					"extractor matches no authserv-id and every verdict degrades to unknown",
					job.Email, tc.wantEmail)
			}
			if tc.wantProvider == "gmail" && job.Username != "" {
				t.Errorf("Username = %q on the gmail branch; it is the IMAP login and this test exists "+
					"because it is EMPTY here, so it can never stand in for the address", job.Username)
			}
		})
	}

	// Belt-and-braces on the tenant pin, since Email is new data leaving this seam.
	other, err := q.CreateWorkspace(ctx, "Poll identity foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	if _, err := core.GetInboxPollJob(ctx, imapID.String(), other.ID.String()); err == nil {
		t.Error("a foreign workspace read the mailbox's poll job, address included")
	}
}

func pollJobPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func sealMailboxSecret(t *testing.T, sealer *crypto.Sealer, raw []byte) string {
	t.Helper()
	ct, err := sealer.Seal(raw)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ct
}

// marshalUnexpiredToken builds an OAuth token that is still valid, so the token
// source hands it back without contacting Google.
func marshalUnexpiredToken(t *testing.T) []byte {
	t.Helper()
	b, err := mail.MarshalToken(&oauth2.Token{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}
	return b
}

// pollJobMailbox creates a mailbox on a chosen provider. The IMAP columns are left
// as an OAuth mailbox really has them — imap_username EMPTY — which is the whole
// point of the assertion above.
func pollJobMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, provider, email, ciphertext string) uuid.UUID {
	t.Helper()
	params := gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: provider, Email: email, DisplayName: email,
		SmtpHost: "smtp.recv.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.recv.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: ciphertext, DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	}
	if provider != "smtp" {
		params.SmtpHost, params.SmtpUsername = "", ""
		params.ImapHost, params.ImapUsername = "", ""
		params.SmtpPort, params.ImapPort = 0, 0
	}
	mb, err := q.CreateMailbox(ctx, params)
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	return mb.ID
}

// Compile-time assertion that the job type still carries the field this file is
// about, so removing it fails here loudly rather than leaving these tests to be
// deleted as "no longer relevant".
var _ = coreapi.InboxPollJob{}.Email
