package audit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
)

func validEvent() Event {
	return Event{
		WorkspaceID: uuid.New(),
		Actor:       UserActor(uuid.New()),
		Action:      ActionCampaignPaused,
		TargetType:  "campaign",
		TargetID:    uuid.NewString(),
		Metadata:    Metadata{"name": "Q3 outreach"},
	}
}

func TestValidateAcceptsAWellFormedEvent(t *testing.T) {
	if err := Validate(validEvent()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Event){
		"no workspace":        func(e *Event) { e.WorkspaceID = uuid.Nil },
		"unknown action":      func(e *Event) { e.Action = "campaign.exploded" },
		"unknown actor type":  func(e *Event) { e.Actor.Type = "robot" },
		"password key":        func(e *Event) { e.Metadata = Metadata{"new_password": "x"} },
		"token key":           func(e *Event) { e.Metadata = Metadata{"refresh_token": "x"} },
		"secret key":          func(e *Event) { e.Metadata = Metadata{"client_secret": "x"} },
		"body key":            func(e *Event) { e.Metadata = Metadata{"reply_body": "x"} },
		"non snake key":       func(e *Event) { e.Metadata = Metadata{"Name": "x"} },
		"oversized value":     func(e *Event) { e.Metadata = Metadata{"name": strings.Repeat("a", 513)} },
		"oversized target id": func(e *Event) { e.TargetID = strings.Repeat("a", 201) },
		"too many keys": func(e *Event) {
			e.Metadata = Metadata{}
			for i := range 17 {
				e.Metadata["k"+strings.Repeat("a", i)] = "v"
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ev := validEvent()
			mutate(&ev)
			if err := Validate(ev); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestEveryActionIsDottedAndUnique(t *testing.T) {
	seen := map[Action]bool{}
	for _, a := range AllActions {
		if seen[a] {
			t.Errorf("duplicate action %q", a)
		}
		seen[a] = true
		if !strings.Contains(string(a), ".") {
			t.Errorf("action %q is not dotted", a)
		}
	}
}

func TestNewAttributesFromContext(t *testing.T) {
	uid := uuid.New()
	ctx := WithActor(context.Background(), UserActor(uid))
	ctx = WithRequest(ctx, "203.0.113.9", "curl/8")
	ws := uuid.New()

	ev := New(ctx, ws, ActionAuthLogin, "user", uid.String(), nil)

	if ev.Actor.Type != ActorUser || ev.Actor.ID != uid.String() || ev.Actor.UserID == nil || *ev.Actor.UserID != uid {
		t.Fatalf("actor = %+v, want user %s", ev.Actor, uid)
	}
	if ev.IP != "203.0.113.9" || ev.UserAgent != "curl/8" {
		t.Fatalf("request meta = %q/%q", ev.IP, ev.UserAgent)
	}
	if ev.WorkspaceID != ws {
		t.Fatalf("workspace = %s, want %s", ev.WorkspaceID, ws)
	}
}

func TestNewWithoutActorIsSystem(t *testing.T) {
	ev := New(context.Background(), uuid.New(), ActionCampaignPaused, "", "", nil)
	if ev.Actor.Type != ActorSystem || ev.Actor.UserID != nil {
		t.Fatalf("actor = %+v, want system", ev.Actor)
	}
}

func TestInsertParamsNormalises(t *testing.T) {
	ev := validEvent()
	ev.IP = "not-an-ip"
	ev.UserAgent = strings.Repeat("é", 400) // 800 bytes, multi-byte runes
	ev.Metadata = nil

	p, err := insertParams(ev)
	if err != nil {
		t.Fatalf("insertParams: %v", err)
	}
	if p.Ip != nil {
		t.Errorf("garbage ip stored as %v, want NULL", p.Ip)
	}
	if len(p.UserAgent) > maxUserAgentLen || !strings.HasPrefix(ev.UserAgent, p.UserAgent) {
		t.Errorf("user agent not truncated on a rune boundary: %d bytes", len(p.UserAgent))
	}
	if string(p.Metadata) != "{}" {
		t.Errorf("nil metadata encoded as %s, want {}", p.Metadata)
	}
	if !p.ActorUserID.Valid {
		t.Error("actor user id dropped")
	}
}

type fakeRecorder struct {
	events []Event
	err    error
	ctxErr error
}

func (f *fakeRecorder) Record(ctx context.Context, ev Event) error {
	f.ctxErr = ctx.Err()
	f.events = append(f.events, ev)
	return f.err
}

func TestEmitSurvivesACancelledRequest(t *testing.T) {
	rec := &fakeRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	Emit(ctx, rec, validEvent())

	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	if rec.ctxErr != nil {
		t.Fatalf("write ran on a cancelled context: %v", rec.ctxErr)
	}
}

func TestEmitSwallowsErrorsAndToleratesNil(t *testing.T) {
	Emit(context.Background(), nil, validEvent()) // must not panic
	rec := &fakeRecorder{err: errors.New("db down")}
	Emit(context.Background(), rec, validEvent()) // must not panic or propagate
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
}

func TestRequestMiddlewareStoresClientMeta(t *testing.T) {
	var gotIP, gotUA string
	h := RequestMiddleware(httpx.NewClientIPResolver(nil))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotIP, gotUA = RequestFrom(r.Context())
	}))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "198.51.100.7:4242"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("X-Forwarded-For", "10.9.9.9") // untrusted peer: must be ignored
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotIP != "198.51.100.7" {
		t.Errorf("ip = %q, want the peer address (XFF from an untrusted peer ignored)", gotIP)
	}
	if gotUA != "Mozilla/5.0" {
		t.Errorf("user agent = %q", gotUA)
	}
}
