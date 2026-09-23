//go:build integration

package inbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The thread reader shows the copy a contact was actually SENT. Step 1 went
// out as A/B variant B, so the reader must show B's subject and body — not the
// step's base copy, which this contact never received. Step 2 went out as the
// base copy (variant_id NULL) and must show that, with the empty-subject
// "Re: <step 1 subject>" rule still resolving against step 1's BASE subject,
// exactly as the send path does.
func TestGetThreadShowsTheVariantASendActuallyCarriedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, steps := searchCampaign(t, ctx, f, "Base subject", "Base copy for step one.", "Base copy for step two.")
	variant, err := f.q.CreateStepVariant(ctx, gen.CreateStepVariantParams{
		WorkspaceID: f.ws, StepID: steps[0], Label: "B", Weight: 1,
		Subject: "Variant subject", BodyText: "Variant copy for step one.", BodyHtml: "<p>Variant copy</p>",
	})
	if err != nil {
		t.Fatalf("variant: %v", err)
	}
	sentAt := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, message_id, sent_at, step_order, variant_id)
		 VALUES ($1,$2,$3,$4,'them@example.com','sent',$5,$6,1,$7)`,
		f.ws, campaignID, contactID, f.mailbox, "<v-"+uuid.NewString()+"@sender.test>", sentAt, variant.ID,
	); err != nil {
		t.Fatalf("insert variant send: %v", err)
	}
	insertSentStep(t, ctx, f, campaignID, contactID, 2, sentAt.Add(time.Hour))
	th := campaignThread(t, ctx, f, campaignID, contactID, "ok", sentAt.Add(90*time.Minute))

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	var outbound []inbox.Message
	for _, m := range detail.Messages {
		if m.Direction == "outbound" {
			outbound = append(outbound, m)
		}
	}
	if len(outbound) != 2 {
		t.Fatalf("outbound messages = %+v, want both sent steps", outbound)
	}
	step1, step2 := outbound[0], outbound[1]
	if step1.Subject != "Variant subject" || step1.BodyText != "Variant copy for step one." || step1.BodyHTML != "<p>Variant copy</p>" {
		t.Errorf("step 1 = %q / %q / %q, want the variant this contact was sent", step1.Subject, step1.BodyText, step1.BodyHTML)
	}
	if step2.Subject != "Re: Base subject" || step2.BodyText != "Base copy for step two." {
		t.Errorf("step 2 = %q / %q, want the base copy with the Re: rule on step 1's base subject", step2.Subject, step2.BodyText)
	}
}
