package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/credbroker"
)

// fakeSuppression is the CONTROL plane's side in this file: it records what it
// was asked and returns a fixed answer.
//
// There is no database, no pool and no migration anywhere in this file, and
// that is the point rather than a convenience. The thing under test is a client
// that HAS no database access — if it can answer IsSuppressed correctly here,
// it answers it without one.
type fakeSuppression struct {
	gotWS    uuid.UUID
	gotEmail string
	calls    int
	answer   bool
	err      error
}

func (f *fakeSuppression) IsSuppressed(_ context.Context, ws uuid.UUID, email string) (bool, error) {
	f.calls++
	f.gotWS, f.gotEmail = ws, email
	return f.answer, f.err
}

// testToken is the shared fleet bearer credential, at the floor length.
const testToken = "0123456789abcdef0123456789abcdef"

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// serve stands the handler up on an httptest server and returns a client
// pointed at it. allowPlaintext is true because httptest speaks http.
func serve(t *testing.T, r SuppressionReader, clientToken string) (*Client, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(r, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, clientToken, true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// The headline: a client holding nothing but a URL and a token answers the
// question correctly, in both directions, through the real handler.
func TestSuppressionCheckRoundTripsWithoutADatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer bool
	}{
		{"suppressed", true},
		{"not suppressed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSuppression{answer: tc.answer}
			c, _ := serve(t, f, testToken)

			ws := uuid.New()
			got, err := c.IsSuppressed(context.Background(), ws.String(), "ada@example.test")
			if err != nil {
				t.Fatalf("IsSuppressed: %v", err)
			}
			if got != tc.answer {
				t.Errorf("suppressed = %v, want %v", got, tc.answer)
			}
			if f.calls != 1 {
				t.Errorf("the control plane was asked %d times, want 1", f.calls)
			}
			if f.gotWS != ws {
				t.Errorf("control plane saw workspace %v, want %v", f.gotWS, ws)
			}
			if f.gotEmail != "ada@example.test" {
				t.Errorf("control plane saw email %q, want ada@example.test", f.gotEmail)
			}
		})
	}
}

// The wire contract, asserted on the BYTES rather than on the fake: the request
// carries the workspace and the one address being asked about, and nothing
// else. A worker must never be able to send a filter, a pattern or a limit —
// that is what keeps this a question about one named subject instead of a
// general query engine (the same rule credbroker's ids-only request follows).
func TestTheRequestBodyCarriesTheWorkspaceAndTheAddressOnly(t *testing.T) {
	ws := uuid.New()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(suppressionResponse{Suppressed: false})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.IsSuppressed(context.Background(), ws.String(), "ada@example.test"); err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if len(fields) != 2 || fields["workspace_id"] != ws.String() || fields["email"] != "ada@example.test" {
		t.Errorf("request body = %s, want exactly workspace_id + email", body)
	}
}

// Fail closed, the load-bearing property: a worker that cannot reach the
// control plane must REFUSE, never answer "not suppressed". A false negative
// here is mail sent to someone who opted out.
func TestAnUnreachableControlPlaneIsAnErrorNeverAFalseNegative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := NewClient(srv.URL, testToken, true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close() // nothing is listening from here on

	suppressed, err := c.IsSuppressed(context.Background(), uuid.New().String(), "ada@example.test")
	if err == nil {
		t.Fatal("IsSuppressed succeeded against a dead control plane, want an error")
	}
	if suppressed {
		t.Errorf("suppressed = true on a failed call; the bool must not be read, but false+err is the contract")
	}
}

// Same property one layer up: a control plane that answers, but answers badly,
// is still a refusal rather than a "no".
func TestAControlPlaneFailureIsNeverAFalseNegative(t *testing.T) {
	f := &fakeSuppression{err: errors.New("connection pool exhausted"), answer: true}
	c, _ := serve(t, f, testToken)

	suppressed, err := c.IsSuppressed(context.Background(), uuid.New().String(), "ada@example.test")
	if err == nil {
		t.Fatal("IsSuppressed succeeded despite a store failure, want an error")
	}
	if suppressed {
		t.Error("suppressed = true, want the zero value alongside the error")
	}
	// And the control plane's own error text is not relayed into a worker's
	// logs: an upstream string can carry request detail, and a seam that echoed
	// "no rows in result set" would be a probe oracle.
	if strings.Contains(err.Error(), "connection pool exhausted") {
		t.Errorf("the control plane's error text reached the worker: %v", err)
	}
}

// Authentication: a wrong token is rejected AND the reader is never reached, so
// the endpoint answers nothing at all rather than answering a stranger.
func TestAWrongTokenIsRejectedAndAnswersNothing(t *testing.T) {
	f := &fakeSuppression{answer: true}
	c, _ := serve(t, f, strings.Repeat("z", credbroker.MinTokenLen))

	suppressed, err := c.IsSuppressed(context.Background(), uuid.New().String(), "ada@example.test")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if suppressed {
		t.Error("suppressed = true from a rejected call")
	}
	if f.calls != 0 {
		t.Errorf("the reader was reached %d times despite a bad token, want 0", f.calls)
	}
}

// A request with no Authorization header at all is rejected before the body is
// even read, and likewise reaches no reader.
func TestAMissingAuthorizationHeaderIsRejected(t *testing.T) {
	f := &fakeSuppression{answer: true}
	h, err := NewHandler(f, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+PathSuppressionCheck,
		bytes.NewReader([]byte(`{"workspace_id":"`+uuid.New().String()+`","email":"ada@example.test"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if f.calls != 0 {
		t.Errorf("the reader was reached %d times with no token, want 0", f.calls)
	}
}

// A malformed workspace id never reaches the reader: it is rejected at the
// boundary, on both sides. The client rejects it without spending a round trip;
// the handler rejects it too, because a handler may not trust its client.
func TestAMalformedWorkspaceIDNeverReachesTheReader(t *testing.T) {
	f := &fakeSuppression{answer: true}
	c, srv := serve(t, f, testToken)

	if _, err := c.IsSuppressed(context.Background(), "not-a-uuid", "ada@example.test"); err == nil {
		t.Fatal("IsSuppressed accepted a malformed workspace id, want an error")
	}
	if f.calls != 0 {
		t.Fatalf("the reader was reached %d times for a malformed id, want 0", f.calls)
	}

	// Handler side, bypassing the client's own check.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+PathSuppressionCheck, strings.NewReader(`{"workspace_id":"nope","email":"ada@example.test"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if f.calls != 0 {
		t.Errorf("the reader was reached %d times for a malformed id, want 0", f.calls)
	}
}

// An unknown field in the request body is refused rather than ignored. This is
// what stops the endpoint growing an accidental second parameter: a worker that
// sent {"workspace_id":…,"email":…,"limit":100} must get a 400, not a silently
// narrower answer.
func TestAnUnknownRequestFieldIsRefused(t *testing.T) {
	f := &fakeSuppression{}
	_, srv := serve(t, f, testToken)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+PathSuppressionCheck,
		strings.NewReader(`{"workspace_id":"`+uuid.New().String()+`","email":"a@b.test","limit":100}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if f.calls != 0 {
		t.Errorf("the reader was reached %d times, want 0", f.calls)
	}
}

// The transport must be behaviour-PARITY with the in-process call, not merely
// similar: whatever string the worker would have handed the local query, the
// control plane hands the same query. An empty address is the case where a
// well-meaning extra validation would diverge — in-process it is simply an
// address nothing matches.
func TestAnEmptyAddressIsPassedThroughRatherThanSpecialCased(t *testing.T) {
	f := &fakeSuppression{answer: false}
	c, _ := serve(t, f, testToken)

	got, err := c.IsSuppressed(context.Background(), uuid.New().String(), "")
	if err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}
	if got {
		t.Errorf("suppressed = true for an empty address")
	}
	if f.calls != 1 || f.gotEmail != "" {
		t.Errorf("reader saw %d calls with email %q, want 1 call with the empty string", f.calls, f.gotEmail)
	}
}

// The fleet channel is https unless an operator explicitly chose otherwise —
// the identical rule, and the identical reasoning, as the credential broker
// that shares this listener and this token.
func TestAPlaintextURLIsRefusedUnlessChosen(t *testing.T) {
	for _, tc := range []struct {
		name           string
		url            string
		allowPlaintext bool
		wantErr        error
	}{
		{"http refused by default", "http://control.internal:8090", false, credbroker.ErrInsecureURL},
		{"http allowed when chosen", "http://control.internal:8090", true, nil},
		{"https always fine", "https://control.example", false, nil},
		{"no scheme is refused, never assumed", "control.example:8090", false, credbroker.ErrInsecureURL},
		{"a non-http scheme is refused", "ftp://control.example", true, credbroker.ErrInsecureURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(tc.url, testToken, tc.allowPlaintext)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestAWeakTokenIsRefusedOnBothSides(t *testing.T) {
	short := strings.Repeat("a", credbroker.MinTokenLen-1)
	if _, err := NewClient("https://control.example", short, false); !errors.Is(err, credbroker.ErrWeakToken) {
		t.Errorf("client err = %v, want ErrWeakToken", err)
	}
	if _, err := NewHandler(&fakeSuppression{}, short, quietLogger()); !errors.Is(err, credbroker.ErrWeakToken) {
		t.Errorf("handler err = %v, want ErrWeakToken", err)
	}
}

// A redirect is never followed: doing so would replay the fleet bearer token at
// whatever host the redirect named, and accept that host's answer as the
// workspace's suppression state.
func TestARedirectIsNotFollowed(t *testing.T) {
	var hits int
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(suppressionResponse{Suppressed: false})
	}))
	defer evil.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+PathSuppressionCheck, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	c, err := NewClient(redirector.URL, testToken, true)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.IsSuppressed(context.Background(), uuid.New().String(), "ada@example.test"); err == nil {
		t.Fatal("want an error, got an answer from the redirect target")
	}
	if hits != 0 {
		t.Errorf("the redirect target was dialed %d times, want 0", hits)
	}
}

// A cancelled caller context cancels the call rather than being ignored —
// the asynq task ceiling above this one is what makes that matter.
func TestTheCallerContextIsHonoured(t *testing.T) {
	f := &fakeSuppression{}
	c, _ := serve(t, f, testToken)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.IsSuppressed(ctx, uuid.New().String(), "ada@example.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
