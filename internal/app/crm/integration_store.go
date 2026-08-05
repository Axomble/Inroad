package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCaptureDisabled = errors.New("crm: automatic capture disabled")

type integrationStore interface {
	GetBoard(context.Context, uuid.UUID, *uuid.UUID) (Board, error)
	MoveDeal(context.Context, uuid.UUID, uuid.UUID, MoveDealInput) (Deal, error)
	GetSettings(context.Context, uuid.UUID) (CRMSettings, error)
	UpdateSettings(context.Context, uuid.UUID, string) (CRMSettings, error)
	CapturePositiveReply(context.Context, uuid.UUID, CaptureReplyInput) (Deal, error)
	ListDealThreads(context.Context, uuid.UUID, uuid.UUID) ([]Thread, error)
	ListEvents(context.Context, uuid.UUID, Target, int32) ([]Event, error)
	AppendEvent(context.Context, uuid.UUID, EventInput) error
}

func (s *PgStore) GetBoard(ctx context.Context, workspaceID uuid.UUID, pipelineID *uuid.UUID) (Board, error) {
	pipelines, err := s.ListPipelines(ctx, workspaceID, maxPipelines)
	if err != nil {
		return Board{}, err
	}
	var selected *Pipeline
	for i := range pipelines {
		if (pipelineID != nil && pipelines[i].ID == *pipelineID) || (pipelineID == nil && pipelines[i].IsDefault) {
			selected = &pipelines[i]
			break
		}
	}
	if selected == nil {
		return Board{}, ErrNotFound
	}
	deals, err := s.ListDeals(ctx, workspaceID, PageRequest{Limit: boardDealLimit})
	if err != nil {
		return Board{}, err
	}
	board := Board{Pipeline: *selected, Stages: make([]BoardStage, len(selected.Stages))}
	byStage := make(map[uuid.UUID]int, len(selected.Stages))
	for i, stage := range selected.Stages {
		board.Stages[i] = BoardStage{Stage: stage, Deals: []Deal{}}
		byStage[stage.ID] = i
	}
	for _, deal := range deals.Items {
		if deal.PipelineID != selected.ID {
			continue
		}
		i, ok := byStage[deal.StageID]
		if !ok {
			continue
		}
		board.Stages[i].Deals = append(board.Stages[i].Deals, deal)
		board.Stages[i].DealCount++
		if deal.AmountMicros != nil {
			board.Stages[i].AmountMicros += *deal.AmountMicros
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT stage_id,count(*)::bigint,COALESCE(sum(amount_micros),0)::bigint
 FROM deals WHERE workspace_id=$1 AND pipeline_id=$2 GROUP BY stage_id`, workspaceID, selected.ID)
	if err != nil {
		return Board{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var stageID uuid.UUID
		var count, amount int64
		if err := rows.Scan(&stageID, &count, &amount); err != nil {
			return Board{}, err
		}
		if i, ok := byStage[stageID]; ok {
			board.Stages[i].DealCount = count
			board.Stages[i].AmountMicros = amount
		}
	}
	if err := rows.Err(); err != nil {
		return Board{}, err
	}
	return board, nil
}

func (s *PgStore) MoveDeal(ctx context.Context, workspaceID, dealID uuid.UUID, in MoveDealInput) (Deal, error) {
	actor, err := in.Actor.JSON()
	if err != nil {
		return Deal{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	var pipelineID, oldStageID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT name, pipeline_id, stage_id FROM deals WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, dealID).
		Scan(&name, &pipelineID, &oldStageID); err != nil {
		return Deal{}, notFound(err)
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_stages WHERE workspace_id=$1 AND pipeline_id=$2 AND id=$3)`,
		workspaceID, pipelineID, in.StageID).Scan(&valid); err != nil || !valid {
		if err != nil {
			return Deal{}, err
		}
		return Deal{}, ErrValidation
	}
	var beforeID, afterID any
	if in.BeforeDealID != nil {
		beforeID = *in.BeforeDealID
	}
	if in.AfterDealID != nil {
		afterID = *in.AfterDealID
	}
	const moveSQL = `
WITH bounds AS (
 SELECT
  (SELECT position FROM deals WHERE workspace_id=$1 AND stage_id=$3 AND id=$4) AS before_pos,
  (SELECT position FROM deals WHERE workspace_id=$1 AND stage_id=$3 AND id=$5) AS after_pos,
  (SELECT COALESCE(max(position),0) FROM deals WHERE workspace_id=$1 AND stage_id=$3) AS max_pos
)
UPDATE deals SET stage_id=$3, position=CASE
 WHEN bounds.before_pos IS NOT NULL AND bounds.after_pos IS NOT NULL THEN (bounds.before_pos+bounds.after_pos)/2
 WHEN bounds.before_pos IS NOT NULL THEN bounds.before_pos+1000
 WHEN bounds.after_pos IS NOT NULL THEN bounds.after_pos-1000
 ELSE bounds.max_pos+1000 END
FROM bounds WHERE deals.workspace_id=$1 AND deals.id=$2`
	if tag, execErr := tx.Exec(ctx, moveSQL, workspaceID, dealID, in.StageID, beforeID, afterID); execErr != nil || tag.RowsAffected() != 1 {
		if execErr != nil {
			return Deal{}, execErr
		}
		return Deal{}, ErrNotFound
	}
	data, _ := json.Marshal(map[string]any{"from_stage_id": oldStageID, "to_stage_id": in.StageID})
	if oldStageID != in.StageID {
		_, err = tx.Exec(ctx, `INSERT INTO events
 (workspace_id,name,kind,object_type,object_id,deal_id,actor,data,linked_record_cached_name)
 VALUES($1,'deal.stage_changed','stage_change','deal',$2,$2,$3,$4,$5)`,
			workspaceID, dealID, actor, data, name)
		if err != nil {
			return Deal{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Deal{}, err
	}
	return s.GetDeal(ctx, workspaceID, dealID)
}

func (s *PgStore) GetSettings(ctx context.Context, workspaceID uuid.UUID) (CRMSettings, error) {
	var out CRMSettings
	err := s.pool.QueryRow(ctx, `INSERT INTO workspace_crm_settings(workspace_id) VALUES($1)
 ON CONFLICT(workspace_id) DO UPDATE SET workspace_id=EXCLUDED.workspace_id
 RETURNING auto_capture_policy,updated_at`, workspaceID).Scan(&out.AutoCapturePolicy, &out.UpdatedAt)
	return out, err
}

func (s *PgStore) UpdateSettings(ctx context.Context, workspaceID uuid.UUID, policy string) (CRMSettings, error) {
	var out CRMSettings
	err := s.pool.QueryRow(ctx, `INSERT INTO workspace_crm_settings(workspace_id,auto_capture_policy) VALUES($1,$2)
 ON CONFLICT(workspace_id) DO UPDATE SET auto_capture_policy=EXCLUDED.auto_capture_policy
 RETURNING auto_capture_policy,updated_at`, workspaceID, policy).Scan(&out.AutoCapturePolicy, &out.UpdatedAt)
	return out, err
}

func (s *PgStore) AppendEvent(ctx context.Context, workspaceID uuid.UUID, in EventInput) error {
	actor, err := in.Actor.JSON()
	if err != nil {
		return err
	}
	if len(in.Data) == 0 {
		in.Data = json.RawMessage(`{}`)
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO events
 (workspace_id,name,kind,object_type,object_id,contact_id,company_id,deal_id,actor,data,
  linked_record_cached_name,source_message_id,source_thread_ref,occurred_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT DO NOTHING`,
		workspaceID, in.Name, in.Kind, in.ObjectType, in.ObjectID, in.ContactID, in.CompanyID, in.DealID,
		actor, in.Data, in.LinkedRecordCachedName, in.SourceMessageID, in.SourceThreadRef, in.OccurredAt)
	return err
}

func (s *PgStore) ListEvents(ctx context.Context, workspaceID uuid.UUID, target Target, limit int32) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT * FROM (
 SELECT id,name,kind,object_type,object_id,contact_id,company_id,deal_id,
  actor,data,linked_record_cached_name,source_message_id,source_thread_ref,occurred_at
 FROM events WHERE workspace_id=$1 AND (
  ($2='deal' AND deal_id=$3) OR ($2='company' AND company_id=$3) OR ($2='contact' AND contact_id=$3))
 UNION ALL
 SELECT s.id,'message.sent','sent','message',s.id,d.primary_contact_id,d.company_id,d.id,
  '{"type":"system","id":"sending"}'::jsonb,'{}'::jsonb,d.name,s.message_id,d.source_thread_ref,
  COALESCE(s.sent_at,s.created_at)
 FROM deals d JOIN sends s ON s.workspace_id=d.workspace_id
  AND s.contact_id=d.primary_contact_id AND s.campaign_id=d.source_campaign_id
 WHERE $2='deal' AND d.workspace_id=$1 AND d.id=$3 AND s.status='sent'
 UNION ALL
 SELECT te.id,CASE WHEN te.kind='open' THEN 'message.opened' ELSE 'message.clicked' END,
  te.kind::text,'message',te.send_id,d.primary_contact_id,d.company_id,d.id,
  '{"type":"system","id":"tracking"}'::jsonb,jsonb_build_object('url',te.url),d.name,s.message_id,
  d.source_thread_ref,te.created_at
 FROM deals d JOIN sends s ON s.workspace_id=d.workspace_id
  AND s.contact_id=d.primary_contact_id AND s.campaign_id=d.source_campaign_id
 JOIN tracking_events te ON te.workspace_id=s.workspace_id AND te.send_id=s.id
 WHERE $2='deal' AND d.workspace_id=$1 AND d.id=$3
 ) activity ORDER BY occurred_at DESC,id DESC LIMIT $4`, workspaceID, target.Type, target.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Event, 0)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Name, &event.Kind, &event.ObjectType, &event.ObjectID,
			&event.ContactID, &event.CompanyID, &event.DealID, &event.Actor, &event.Data,
			&event.LinkedRecordCachedName, &event.SourceMessageID, &event.SourceThreadRef, &event.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *PgStore) ListDealThreads(ctx context.Context, workspaceID, dealID uuid.UUID) ([]Thread, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id,t.thread_ref,t.subject,t.reply_class,t.campaign_id,t.contact_id,t.last_message_at
 FROM crm_threads t JOIN deal_threads dt ON dt.workspace_id=t.workspace_id AND dt.thread_id=t.id
 WHERE t.workspace_id=$1 AND dt.deal_id=$2 ORDER BY t.last_message_at DESC`, workspaceID, dealID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Thread, 0)
	for rows.Next() {
		var thread Thread
		if err := rows.Scan(&thread.ID, &thread.ThreadRef, &thread.Subject, &thread.ReplyClass,
			&thread.CampaignID, &thread.ContactID, &thread.LastMessageAt); err != nil {
			return nil, err
		}
		thread.Participants, err = s.threadParticipants(ctx, workspaceID, thread.ID)
		if err != nil {
			return nil, err
		}
		thread.Messages, err = s.threadMessages(ctx, workspaceID, thread.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, thread)
	}
	return out, rows.Err()
}

func (s *PgStore) threadParticipants(ctx context.Context, workspaceID, threadID uuid.UUID) ([]ThreadParticipant, error) {
	rows, err := s.pool.Query(ctx, `SELECT email::text,display_name,contact_id FROM crm_thread_participants
 WHERE workspace_id=$1 AND thread_id=$2 ORDER BY lower(email::text)`, workspaceID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ThreadParticipant, 0)
	for rows.Next() {
		var value ThreadParticipant
		if err := rows.Scan(&value.Email, &value.DisplayName, &value.ContactID); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *PgStore) threadMessages(ctx context.Context, workspaceID, threadID uuid.UUID) ([]ThreadMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,direction,kind,message_id,sender_email,recipient_email,subject,reply_class,occurred_at
 FROM crm_messages WHERE workspace_id=$1 AND thread_id=$2 ORDER BY occurred_at,id`, workspaceID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ThreadMessage, 0)
	for rows.Next() {
		var value ThreadMessage
		if err := rows.Scan(&value.ID, &value.Direction, &value.Kind, &value.MessageID, &value.SenderEmail,
			&value.RecipientEmail, &value.Subject, &value.ReplyClass, &value.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

type captureContext struct {
	ContactID    uuid.UUID
	ContactEmail string
	FirstName    string
	LastName     string
	CompanyText  string
	CompanyID    *uuid.UUID
	CampaignID   uuid.UUID
	CampaignName string
	OutboundID   string
	OutboundAt   *time.Time
	MailboxEmail string
}

func (s *PgStore) CapturePositiveReply(ctx context.Context, workspaceID uuid.UUID, in CaptureReplyInput) (Deal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var policy string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace_crm_settings(workspace_id) VALUES($1)
 ON CONFLICT(workspace_id) DO UPDATE SET workspace_id=EXCLUDED.workspace_id RETURNING auto_capture_policy`,
		workspaceID).Scan(&policy); err != nil {
		return Deal{}, err
	}
	if policy == "off" {
		return Deal{}, ErrCaptureDisabled
	}
	var capture captureContext
	const contextSQL = `SELECT c.id,c.email,c.first_name,c.last_name,c.company,c.company_id,
 ca.id,ca.name,s.message_id,s.sent_at,m.email
 FROM sequence_enrollments e
 JOIN contacts c ON c.workspace_id=e.workspace_id AND c.id=e.contact_id
 JOIN campaigns ca ON ca.workspace_id=e.workspace_id AND ca.id=e.campaign_id
 JOIN sends s ON s.workspace_id=e.workspace_id AND s.id=$3
  AND s.campaign_id=e.campaign_id AND s.contact_id=e.contact_id
 JOIN mailboxes m ON m.workspace_id=s.workspace_id AND m.id=s.mailbox_id
 WHERE e.workspace_id=$1 AND e.id=$2`
	if err := tx.QueryRow(ctx, contextSQL, workspaceID, in.EnrollmentID, in.SendID).Scan(
		&capture.ContactID, &capture.ContactEmail, &capture.FirstName, &capture.LastName, &capture.CompanyText,
		&capture.CompanyID, &capture.CampaignID, &capture.CampaignName, &capture.OutboundID,
		&capture.OutboundAt, &capture.MailboxEmail); err != nil {
		return Deal{}, notFound(err)
	}
	companyID, err := ensureCapturedCompany(ctx, tx, workspaceID, capture, in.SenderEmail)
	if err != nil {
		return Deal{}, err
	}
	var pipelineID, stageID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT p.id,s.id FROM pipelines p JOIN pipeline_stages s
 ON s.workspace_id=p.workspace_id AND s.pipeline_id=p.id
 WHERE p.workspace_id=$1 AND p.is_default ORDER BY s.position LIMIT 1`, workspaceID).Scan(&pipelineID, &stageID); err != nil {
		return Deal{}, err
	}
	when := in.OccurredAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	threadRef := strings.TrimSpace(in.ThreadRef)
	if threadRef == "" {
		threadRef = in.SendID.String()
	}
	var threadID uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO crm_threads
 (workspace_id,thread_ref,subject,reply_class,campaign_id,contact_id,last_message_at)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id,thread_ref) DO UPDATE SET
 subject=EXCLUDED.subject,reply_class=EXCLUDED.reply_class,campaign_id=EXCLUDED.campaign_id,
 contact_id=EXCLUDED.contact_id,last_message_at=GREATEST(crm_threads.last_message_at,EXCLUDED.last_message_at)
 RETURNING id`, workspaceID, threadRef, in.Subject, in.ReplyClass, capture.CampaignID, capture.ContactID, when).Scan(&threadID); err != nil {
		return Deal{}, err
	}
	participants := []struct{ email, name string }{
		{strings.ToLower(strings.TrimSpace(in.SenderEmail)), strings.TrimSpace(in.SenderDisplayName)},
		{strings.ToLower(strings.TrimSpace(capture.MailboxEmail)), ""},
	}
	for _, participant := range participants {
		if participant.email == "" {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO crm_thread_participants(workspace_id,thread_id,email,display_name,contact_id)
 VALUES($1,$2,$3,$4,(SELECT contact_id FROM contact_emails WHERE workspace_id=$1 AND lower(email::text)=lower($3) LIMIT 1))
 ON CONFLICT(workspace_id,thread_id,email) DO UPDATE SET
 display_name=COALESCE(NULLIF(EXCLUDED.display_name,''),crm_thread_participants.display_name),
 contact_id=COALESCE(EXCLUDED.contact_id,crm_thread_participants.contact_id)`,
			workspaceID, threadID, participant.email, participant.name)
		if err != nil {
			return Deal{}, err
		}
	}
	if capture.OutboundID != "" {
		outboundAt := when
		if capture.OutboundAt != nil {
			outboundAt = *capture.OutboundAt
		}
		_, err = tx.Exec(ctx, `INSERT INTO crm_messages
 (workspace_id,thread_id,direction,kind,message_id,sender_email,recipient_email,subject,occurred_at)
 VALUES($1,$2,'outbound','sent',$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
			workspaceID, threadID, capture.OutboundID, capture.MailboxEmail, capture.ContactEmail, in.Subject, outboundAt)
		if err != nil {
			return Deal{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO crm_messages
 (workspace_id,thread_id,direction,kind,message_id,sender_email,recipient_email,subject,reply_class,occurred_at)
 VALUES($1,$2,'inbound','reply',$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
		workspaceID, threadID, in.MessageID, in.SenderEmail, in.RecipientEmail, in.Subject, in.ReplyClass, when)
	if err != nil {
		return Deal{}, err
	}
	dealName := strings.TrimSpace(capture.FirstName + " " + capture.LastName)
	if dealName == "" {
		dealName = capture.ContactEmail
	}
	dealName += " - " + capture.CampaignName
	actor, _ := json.Marshal(Actor{Type: "system", ID: "reply_auto_capture"})
	var dealID uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO deals
 (workspace_id,pipeline_id,stage_id,company_id,primary_contact_id,name,currency,position,source,
  source_campaign_id,source_thread_ref,source_message_id,created_by_actor)
 VALUES($1,$2,$3,$4,$5,$6,'USD',
  (SELECT COALESCE(max(position),0)+1000 FROM deals WHERE workspace_id=$1 AND pipeline_id=$2 AND stage_id=$3),
  'reply',$7,$8,$9,$10)
 ON CONFLICT(workspace_id,source_thread_ref,primary_contact_id)
 WHERE source='reply' AND source_thread_ref<>'' AND primary_contact_id IS NOT NULL
 DO UPDATE SET source_message_id=COALESCE(NULLIF(EXCLUDED.source_message_id,''),deals.source_message_id)
 RETURNING id`, workspaceID, pipelineID, stageID, companyID, capture.ContactID, dealName,
		capture.CampaignID, threadRef, in.MessageID, actor).Scan(&dealID); err != nil {
		return Deal{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deal_threads(workspace_id,deal_id,thread_id,source)
 VALUES($1,$2,$3,'auto') ON CONFLICT DO NOTHING`, workspaceID, dealID, threadID); err != nil {
		return Deal{}, err
	}
	data, _ := json.Marshal(map[string]any{"reply_class": in.ReplyClass, "campaign_id": capture.CampaignID})
	_, err = tx.Exec(ctx, `INSERT INTO events
 (workspace_id,name,kind,object_type,object_id,contact_id,company_id,deal_id,actor,data,
 linked_record_cached_name,source_message_id,source_thread_ref,occurred_at)
 VALUES($1,'reply.positive','reply','deal',$2,$3,$4,$2,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`,
		workspaceID, dealID, capture.ContactID, companyID, actor, data, dealName, in.MessageID, threadRef, when)
	if err != nil {
		return Deal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Deal{}, err
	}
	return s.GetDeal(ctx, workspaceID, dealID)
}

func ensureCapturedCompany(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID, capture captureContext, sender string) (*uuid.UUID, error) {
	if capture.CompanyID != nil {
		return capture.CompanyID, nil
	}
	address := strings.ToLower(strings.TrimSpace(sender))
	parts := strings.Split(address, "@")
	if len(parts) != 2 || isGroupAddress(parts[0]) || isFreeMailDomain(parts[1]) {
		return nil, nil
	}
	var teamMember bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members wm JOIN users u ON u.id=wm.user_id
 WHERE wm.workspace_id=$1 AND lower(u.email::text)=lower($2))`, workspaceID, address).Scan(&teamMember); err != nil {
		return nil, err
	}
	if teamMember {
		return nil, nil
	}
	name := strings.TrimSpace(capture.CompanyText)
	if name == "" {
		name = strings.ToUpper(parts[1][:1]) + strings.Split(parts[1], ".")[0][1:]
	}
	var companyID uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO companies(workspace_id,name,domain,currency) VALUES($1,$2,$3,'USD')
 ON CONFLICT(workspace_id,(lower(domain))) WHERE domain IS NOT NULL AND btrim(domain::text)<>''
 DO UPDATE SET domain=EXCLUDED.domain RETURNING id`, workspaceID, name, parts[1]).Scan(&companyID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE contacts SET company_id=$3 WHERE workspace_id=$1 AND id=$2 AND company_id IS NULL`,
		workspaceID, capture.ContactID, companyID); err != nil {
		return nil, err
	}
	return &companyID, nil
}

func isGroupAddress(local string) bool {
	for _, prefix := range []string{"info", "hello", "sales", "support", "contact", "admin", "team", "office", "billing"} {
		if local == prefix || strings.HasPrefix(local, prefix+"+") {
			return true
		}
	}
	return false
}

func isFreeMailDomain(domain string) bool {
	_, found := map[string]struct{}{
		"gmail.com": {}, "googlemail.com": {}, "outlook.com": {}, "hotmail.com": {}, "live.com": {},
		"yahoo.com": {}, "icloud.com": {}, "me.com": {}, "aol.com": {}, "proton.me": {}, "protonmail.com": {},
	}[domain]
	return found
}

func mergeEvents(events []Event) []Event {
	if len(events) < 2 {
		return events
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].OccurredAt.After(events[j].OccurredAt) })
	out := make([]Event, 0, len(events))
	for _, event := range events {
		if len(out) > 0 && mergeableEvents(out[len(out)-1], event) {
			out[len(out)-1].MergedCount++
			continue
		}
		event.MergedCount = 1
		out = append(out, event)
	}
	return out
}

func mergeableEvents(a, b Event) bool {
	return a.Name == b.Name && a.LinkedRecordCachedName == b.LinkedRecordCachedName &&
		string(a.Actor) == string(b.Actor) && a.OccurredAt.Sub(b.OccurredAt) <= 10*time.Minute
}

var _ integrationStore = (*PgStore)(nil)

func integration(s Store) (integrationStore, error) {
	value, ok := s.(integrationStore)
	if !ok {
		return nil, fmt.Errorf("crm: integration store unavailable")
	}
	return value, nil
}
