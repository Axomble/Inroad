package inprocess

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/crm"
	"github.com/inroad/inroad/internal/coreapi"
)

func (c client) CaptureCRMReply(ctx context.Context, in coreapi.CRMReplyInput) error {
	workspaceID, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return err
	}
	enrollmentID, err := uuid.Parse(in.EnrollmentID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(in.SendID)
	if err != nil {
		return err
	}
	service := crm.NewService(crm.NewPgStore(c.pool))
	_, err = service.CapturePositiveReply(ctx, workspaceID, crm.CaptureReplyInput{
		EnrollmentID: enrollmentID, SendID: sendID, ThreadRef: in.ThreadRef, MessageID: in.MessageID,
		Subject: in.Subject, SenderEmail: in.SenderEmail, RecipientEmail: in.RecipientEmail,
		SenderDisplayName: in.SenderDisplayName, ReplyClass: in.ReplyClass, OccurredAt: in.OccurredAt,
	})
	if errors.Is(err, crm.ErrCaptureDisabled) {
		return nil
	}
	return err
}

var _ coreapi.CRMCaptureClient = client{}
