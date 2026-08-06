//go:build integration

package crm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func TestCRMDefaultPipelineDealAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := gen.New(pool)
	first, err := queries.CreateWorkspace(ctx, "CRM first "+uuid.NewString())
	if err != nil {
		t.Fatalf("first workspace: %v", err)
	}
	second, err := queries.CreateWorkspace(ctx, "CRM second "+uuid.NewString())
	if err != nil {
		t.Fatalf("second workspace: %v", err)
	}
	service := NewService(NewPgStore(pool))

	pipelines, err := service.ListPipelines(ctx, first.ID)
	if err != nil {
		t.Fatalf("list pipelines: %v", err)
	}
	if len(pipelines) != 1 || !pipelines[0].IsDefault || len(pipelines[0].Stages) != 5 {
		t.Fatalf("default pipeline not seeded: %+v", pipelines)
	}

	company, err := service.CreateCompany(ctx, first.ID, CompanyInput{Name: "Acme", Domain: "example.com", Currency: "USD"})
	if err != nil {
		t.Fatalf("create company: %v", err)
	}
	if _, err := service.GetCompany(ctx, second.ID, company.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace company visible: %v", err)
	}

	deal, err := service.CreateDeal(ctx, first.ID, DealInput{Name: "Expansion", PipelineID: pipelines[0].ID, StageID: pipelines[0].Stages[0].ID, CompanyID: &company.ID, Currency: "USD", Source: "manual", Actor: Actor{Type: "user", ID: uuid.NewString()}})
	if err != nil {
		t.Fatalf("create deal: %v", err)
	}
	if deal.CompanyName != "Acme" || deal.StageLabel != "Lead" {
		t.Fatalf("deal joins not populated: %+v", deal)
	}
	if _, err := service.GetDeal(ctx, second.ID, deal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace deal visible: %v", err)
	}
}

func TestPositiveReplyCaptureIsIdempotentAndBuildsDealContext(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := gen.New(pool)
	workspace, err := queries.CreateWorkspace(ctx, "CRM capture "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var mailboxID, listID, contactID, campaignID, enrollmentID, sendID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO mailboxes(workspace_id,email,secret_ciphertext)
 VALUES($1,'seller@inroad.test','sealed') RETURNING id`, workspace.ID).Scan(&mailboxID); err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,'Prospects') RETURNING id`,
		workspace.ID).Scan(&listID); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO contacts(workspace_id,email,first_name,last_name)
 VALUES($1,'buyer@acme.test','Casey','Buyer') RETURNING id`, workspace.ID).Scan(&contactID); err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO campaigns(workspace_id,name,mailbox_id,list_id,subject,status)
 VALUES($1,'Q3 outreach',$2,$3,'A quick question','running') RETURNING id`,
		workspace.ID, mailboxID, listID).Scan(&campaignID); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sequence_enrollments(workspace_id,campaign_id,contact_id)
 VALUES($1,$2,$3) RETURNING id`, workspace.ID, campaignID, contactID).Scan(&enrollmentID); err != nil {
		t.Fatalf("enrollment: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sends
 (workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,message_id,sent_at)
 VALUES($1,$2,$3,$4,'buyer@acme.test','sent','<sent@inroad.test>',now()) RETURNING id`,
		workspace.ID, campaignID, contactID, mailboxID).Scan(&sendID); err != nil {
		t.Fatalf("send: %v", err)
	}
	service := NewService(NewPgStore(pool))
	input := CaptureReplyInput{
		EnrollmentID: enrollmentID, SendID: sendID, ThreadRef: "<sent@inroad.test>",
		MessageID: "<reply@acme.test>", Subject: "Re: A quick question",
		SenderEmail: "buyer@acme.test", RecipientEmail: "seller@inroad.test",
		SenderDisplayName: "Casey Buyer", ReplyClass: "positive", OccurredAt: time.Now().UTC(),
	}
	deal, err := service.CapturePositiveReply(ctx, workspace.ID, input)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	duplicate, err := service.CapturePositiveReply(ctx, workspace.ID, input)
	if err != nil {
		t.Fatalf("duplicate capture: %v", err)
	}
	if duplicate.ID != deal.ID || deal.Source != "reply" || deal.CompanyName != "Acme" {
		t.Fatalf("captured deal = %+v, duplicate = %+v", deal, duplicate)
	}
	threads, err := service.ListDealThreads(ctx, workspace.ID, deal.ID)
	if err != nil {
		t.Fatalf("threads: %v", err)
	}
	if len(threads) != 1 || len(threads[0].Messages) != 2 || len(threads[0].Participants) != 2 {
		t.Fatalf("structured thread = %+v", threads)
	}
	events, err := service.ListEvents(ctx, workspace.ID, Target{Type: TargetDeal, ID: deal.ID})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 2 || !hasEvent(events, "reply.positive") || !hasEvent(events, "message.sent") {
		t.Fatalf("events = %+v", events)
	}
	board, err := service.GetBoard(ctx, workspace.ID, nil)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	moved, err := service.MoveDeal(ctx, workspace.ID, deal.ID, MoveDealInput{
		StageID: board.Pipeline.Stages[1].ID, Actor: Actor{Type: "user", ID: uuid.NewString()},
	})
	if err != nil || moved.StageID != board.Pipeline.Stages[1].ID {
		t.Fatalf("move = %+v, %v", moved, err)
	}
	if _, err := service.UpdateSettings(ctx, workspace.ID, "off"); err != nil {
		t.Fatalf("disable capture: %v", err)
	}
	input.ThreadRef, input.MessageID = "<another@inroad.test>", "<another-reply@acme.test>"
	if _, err := service.CapturePositiveReply(ctx, workspace.ID, input); !errors.Is(err, ErrCaptureDisabled) {
		t.Fatalf("disabled capture = %v", err)
	}
}

func hasEvent(events []Event, name string) bool {
	for _, event := range events {
		if event.Name == name {
			return true
		}
	}
	return false
}
