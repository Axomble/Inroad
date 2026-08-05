package agentrun

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agentchat"
)

type CancelBus interface {
	RegisterCancel(uuid.UUID, context.CancelFunc) func()
	RequestCancel(context.Context, uuid.UUID) error
}

type Manager struct {
	root      context.Context
	runtime   *Runtime
	store     agentchat.Store
	publisher agentchat.StreamPublisher
	cancelBus CancelBus
	logger    *slog.Logger
}

func NewManager(root context.Context, runtime *Runtime, store agentchat.Store, publisher agentchat.StreamPublisher, cancelBus CancelBus, logger *slog.Logger) *Manager {
	return &Manager{root: root, runtime: runtime, store: store, publisher: publisher, cancelBus: cancelBus, logger: logger}
}

func (m *Manager) Start(start agentchat.RunStart) {
	go m.run(start, false)
}

func (m *Manager) Resume(start agentchat.RunStart) { go m.run(start, true) }

func (m *Manager) ContinueQueue(start agentchat.RunStart) {
	go func() {
		ctx, cancel := context.WithTimeout(m.root, 15*time.Second)
		defer cancel()
		m.startNext(ctx, start)
	}()
}

func (m *Manager) ValidateApproval(actor agentchat.Actor, toolName string, arguments []byte) error {
	if m.runtime.Tools == nil {
		return errors.New("agent tools are unavailable")
	}
	return m.runtime.Tools.Validate(actor, toolName, arguments)
}

func (m *Manager) Stop(ctx context.Context, runID uuid.UUID) error {
	return m.cancelBus.RequestCancel(ctx, runID)
}

func (m *Manager) run(start agentchat.RunStart, resume bool) {
	ctx, cancel := context.WithCancel(m.root)
	unregister := m.cancelBus.RegisterCancel(start.RunID, cancel)
	defer unregister()
	defer cancel()
	if !resume {
		if err := m.publisher.Clear(ctx, start.ThreadID); err != nil {
			m.logger.Error("agent stream reset failed", "run_id", start.RunID, "err", err)
		}
	}
	var result Result
	var runErr error
	if resume {
		result, runErr = m.runtime.Resume(ctx, start)
	} else {
		result, runErr = m.runtime.Execute(ctx, start)
	}
	if result.Paused && runErr == nil {
		return
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		m.logger.Error("agent run failed", "run_id", start.RunID, "thread_id", start.ThreadID, "err", runErr)
	}
	cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	status, publicError := agentchat.RunStatusDone, ""
	switch {
	case errors.Is(runErr, context.Canceled):
		status = agentchat.RunStatusCancelled
	case runErr != nil:
		status = agentchat.RunStatusFailed
		publicError = userFacingError(runErr)
	}
	if err := m.store.FinishProcessing(cleanup, start.Actor.WorkspaceID, start.ThreadID); err != nil {
		m.logger.Error("agent message finalization failed", "run_id", start.RunID, "err", err)
	}
	if err := m.store.FinishRun(cleanup, start.Actor.WorkspaceID, start.RunID, status, publicError); err != nil {
		m.logger.Error("agent run finalization failed", "run_id", start.RunID, "err", err)
	}
	_, _ = m.publisher.Publish(cleanup, start.ThreadID, agentchat.Event{Type: agentchat.EventMessagePersisted, RunID: start.RunID.String()})
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		_, _ = m.publisher.Publish(cleanup, start.ThreadID, agentchat.Event{Type: agentchat.EventRunError, RunID: start.RunID.String(), Text: publicError})
	} else {
		if runErr == nil && result.FirstUserText != "" {
			title := m.runtime.GenerateTitle(cleanup, start.Actor.WorkspaceID, result.FirstUserText)
			if changed, err := m.store.SetThreadTitle(cleanup, start.Actor.WorkspaceID, start.ThreadID, title); err == nil && changed {
				_, _ = m.publisher.Publish(cleanup, start.ThreadID, agentchat.Event{Type: agentchat.EventThreadTitle, RunID: start.RunID.String(), Title: title})
			}
		}
		_, _ = m.publisher.Publish(cleanup, start.ThreadID, agentchat.Event{Type: agentchat.EventDone, RunID: start.RunID.String(), ObjectTypes: result.Touched})
	}
	m.startNext(cleanup, start)
}

func (m *Manager) StartExpirySweep() {
	approvals, ok := m.store.(agentchat.ApprovalStore)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-m.root.Done():
				return
			case <-ticker.C:
				starts, err := approvals.ExpirePendingActions(m.root, 100)
				if err != nil {
					m.logger.Error("agent approval expiry sweep failed", "err", err)
					continue
				}
				for _, start := range starts {
					m.Resume(start)
				}
			}
		}
	}()
}

func userFacingError(err error) string {
	if errors.Is(err, agentchat.ErrContextExhausted) {
		return "This conversation is too long to continue. Start a new thread."
	}
	return "The agent could not complete this run. Try again."
}

func (m *Manager) startNext(ctx context.Context, previous agentchat.RunStart) {
	queued, err := m.store.ListQueued(ctx, previous.Actor.WorkspaceID, previous.ThreadID)
	if err != nil {
		m.logger.Error("agent queue read failed", "thread_id", previous.ThreadID, "err", err)
		return
	}
	if len(queued) == 0 {
		return
	}
	selector := queued[0].Row.ModelSelector
	if selector == "" {
		selector = "default-smart-model"
	}
	run, err := m.store.InsertRun(ctx, previous.Actor.WorkspaceID, previous.ThreadID, selector)
	if errors.Is(err, agentchat.ErrRunActive) {
		return
	}
	if err != nil {
		m.logger.Error("agent queued run insert failed", "thread_id", previous.ThreadID, "err", err)
		return
	}
	message, err := m.store.PromoteOldestQueued(ctx, previous.Actor.WorkspaceID, previous.ThreadID)
	if errors.Is(err, agentchat.ErrQueueEmpty) {
		_ = m.store.FinishRun(ctx, previous.Actor.WorkspaceID, run.ID, agentchat.RunStatusCancelled, "queue empty")
		return
	}
	if err != nil {
		_ = m.store.FinishRun(ctx, previous.Actor.WorkspaceID, run.ID, agentchat.RunStatusFailed, "queue promotion failed")
		m.logger.Error("agent queue promotion failed", "thread_id", previous.ThreadID, "err", err)
		return
	}
	if err := m.store.SetThreadActiveRun(ctx, previous.Actor.WorkspaceID, previous.ThreadID, run.ID); err != nil {
		_ = m.store.FinishRun(ctx, previous.Actor.WorkspaceID, run.ID, agentchat.RunStatusFailed, "thread could not start")
		return
	}
	queued, err = m.store.ListQueued(ctx, previous.Actor.WorkspaceID, previous.ThreadID)
	if err == nil {
		_, _ = m.publisher.Publish(ctx, previous.ThreadID, agentchat.Event{Type: agentchat.EventQueueUpdated, RunID: run.ID.String(), Queued: queueDTOs(queued)})
	}
	m.Start(agentchat.RunStart{
		Actor: previous.Actor, ThreadID: previous.ThreadID, RunID: run.ID,
		TurnID: message.TurnID, Selector: selector,
	})
}

func queueDTOs(messages []agentchat.Message) []agentchat.QueuedMessageDTO {
	out := make([]agentchat.QueuedMessageDTO, 0, len(messages))
	for _, message := range messages {
		text := ""
		for _, part := range message.Parts {
			if part.Type == agentchat.PartText {
				text += part.TextContent
			}
		}
		out = append(out, agentchat.QueuedMessageDTO{
			ID: message.Row.ID.String(), Text: text, Model: message.Row.ModelSelector,
			CreatedAt: message.Row.CreatedAt.Time.UTC().Format(time.RFC3339),
		})
	}
	return out
}

var _ agentchat.RunManager = (*Manager)(nil)
