package agentchat

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type serviceStore struct {
	Store
	owner    uuid.UUID
	thread   uuid.UUID
	active   bool
	messages []Message
	deleted  []uuid.UUID
}

func (f *serviceStore) GetThread(_ context.Context, ws, user, id uuid.UUID) (gen.AgentThread, error) {
	if user != f.owner || id != f.thread {
		return gen.AgentThread{}, ErrThreadNotFound
	}
	return gen.AgentThread{ID: id, WorkspaceID: ws, CreatedByUserID: user}, nil
}
func (f *serviceStore) PersistMessage(_ context.Context, in MessageInput) (Message, error) {
	row := gen.AgentMessage{
		ID: uuid.New(), WorkspaceID: in.WorkspaceID, ThreadID: in.ThreadID, TurnID: in.TurnID,
		Role: in.Role, Status: in.Status, ModelSelector: in.ModelSelector, BrowsingContext: in.BrowsingContext,
	}
	message := Message{Row: row}
	for i, part := range in.Parts {
		message.Parts = append(message.Parts, gen.AgentMessagePart{ID: uuid.New(), MessageID: row.ID, OrderIndex: int32(i), Type: part.Type, TextContent: part.Text})
	}
	f.messages = append(f.messages, message)
	return message, nil
}
func (f *serviceStore) InsertRun(_ context.Context, ws, thread uuid.UUID, model string) (gen.AgentRun, error) {
	if f.active {
		return gen.AgentRun{}, ErrRunActive
	}
	f.active = true
	return gen.AgentRun{ID: uuid.New(), WorkspaceID: ws, ThreadID: thread, ModelID: model}, nil
}
func (f *serviceStore) PromoteOldestQueued(context.Context, uuid.UUID, uuid.UUID) (gen.AgentMessage, error) {
	for i := range f.messages {
		if f.messages[i].Row.Status == MessageStatusQueued {
			f.messages[i].Row.Status = MessageStatusProcessing
			return f.messages[i].Row, nil
		}
	}
	return gen.AgentMessage{}, ErrQueueEmpty
}
func (*serviceStore) SetThreadActiveRun(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (f *serviceStore) DeleteQueued(_ context.Context, _, _, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *serviceStore) ListQueued(context.Context, uuid.UUID, uuid.UUID) ([]Message, error) {
	var queued []Message
	for _, message := range f.messages {
		if message.Row.Status == MessageStatusQueued {
			queued = append(queued, message)
		}
	}
	return queued, nil
}

type serviceManager struct{ starts []RunStart }

func (f *serviceManager) Start(start RunStart)                { f.starts = append(f.starts, start) }
func (*serviceManager) Stop(context.Context, uuid.UUID) error { return nil }

type serviceStreams struct{ events []Event }

func (serviceStreams) Attach(context.Context, uuid.UUID, int64) (<-chan Frame, error) {
	ch := make(chan Frame)
	close(ch)
	return ch, nil
}
func (s *serviceStreams) Publish(_ context.Context, _ uuid.UUID, event Event) (int64, error) {
	s.events = append(s.events, event)
	return int64(len(s.events)), nil
}

func TestSendStartsOneRunAndQueuesBehindActiveRun(t *testing.T) {
	owner, thread, workspace := uuid.New(), uuid.New(), uuid.New()
	store := &serviceStore{owner: owner, thread: thread}
	manager := &serviceManager{}
	streams := &serviceStreams{}
	service := NewService(store, manager, streams)
	actor := Actor{WorkspaceID: workspace, UserID: owner, Role: "member"}
	first, err := service.Send(context.Background(), actor, thread, SendInput{Text: " first "})
	if err != nil {
		t.Fatal(err)
	}
	if first.Queued || first.RunID == nil || len(manager.starts) != 1 {
		t.Fatalf("first=%+v starts=%d", first, len(manager.starts))
	}
	second, err := service.Send(context.Background(), actor, thread, SendInput{Text: "second", Model: "provider/model"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Queued || second.RunID != nil || len(manager.starts) != 1 {
		t.Fatalf("second=%+v starts=%d", second, len(manager.starts))
	}
	if store.messages[0].Row.Status != MessageStatusProcessing || store.messages[1].Row.Status != MessageStatusQueued {
		t.Fatalf("statuses=%s,%s", store.messages[0].Row.Status, store.messages[1].Row.Status)
	}
	if store.messages[1].Row.ModelSelector != "provider/model" || len(streams.events) != 1 || streams.events[0].Type != EventQueueUpdated {
		t.Fatalf("queued model=%q events=%+v", store.messages[1].Row.ModelSelector, streams.events)
	}
}

func TestAttachIsOwnerScoped(t *testing.T) {
	store := &serviceStore{owner: uuid.New(), thread: uuid.New()}
	service := NewService(store, &serviceManager{}, &serviceStreams{})
	_, err := service.Attach(context.Background(), Actor{WorkspaceID: uuid.New(), UserID: uuid.New()}, store.thread, 0)
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("error=%v", err)
	}
}
