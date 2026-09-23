package sequencestep

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/seqgraph"
)

// The status + body shape the canvas keys its error handling on.
func TestWriteGraphErrorContract(t *testing.T) {
	loop := []uuid.UUID{uuid.New(), uuid.New()}
	cases := []struct {
		name     string
		err      error
		status   int
		code     string
		stepIDs  int
		hasError bool
	}{
		{"shape", &seqgraph.ShapeError{Code: seqgraph.CodeInvalidWithinDays, Msg: "x"}, http.StatusBadRequest, seqgraph.CodeInvalidWithinDays, 0, true},
		{"cycle", &seqgraph.CycleError{StepIDs: loop}, http.StatusUnprocessableEntity, seqgraph.CodeCycle, 2, true},
		{"wrapped cycle", fmt.Errorf("commit: %w", &seqgraph.CycleError{StepIDs: loop}), http.StatusUnprocessableEntity, seqgraph.CodeCycle, 2, true},
		{"unknown target", &seqgraph.TargetError{StepID: uuid.New(), Target: uuid.New()}, http.StatusUnprocessableEntity, seqgraph.CodeUnknownTarget, 0, true},
		{"target gone", ErrTargetGone, http.StatusUnprocessableEntity, seqgraph.CodeUnknownTarget, 0, true},
		{"campaign missing", ErrCampaignNotFound, http.StatusNotFound, "", 0, true},
		{"step missing", ErrNotFound, http.StatusNotFound, "", 0, true},
		{"not draft", ErrCampaignNotDraft, http.StatusConflict, "", 0, true},
		{"unexpected", errors.New("db down"), http.StatusInternalServerError, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeGraphError(rec, tc.err, "fallback")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var body graphErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if body.Code != tc.code || len(body.StepIDs) != tc.stepIDs || (tc.hasError && body.Error == "") {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

// expected_updated_at has three states in a PUT body, and each means something
// different: confusing absent with null would turn every legacy write into a
// create-only write, and confusing null with absent would drop the guard.
func TestBodyPreconditionDistinguishesAbsentNullAndTimestamp(t *testing.T) {
	at := time.Date(2026, 9, 23, 16, 10, 44, 120000000, time.UTC)
	cases := []struct {
		body string
		want BranchPrecondition
	}{
		{`{"condition":"always"}`, BranchPrecondition{}},
		{`{"condition":"always","expected_updated_at":null}`, ExpectNoBranch()},
		{`{"condition":"always","expected_updated_at":"2026-09-23T16:10:44.12Z"}`, ExpectBranchAt(at)},
	}
	for _, tc := range cases {
		var req branchRequest
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		got, err := bodyPrecondition(req.ExpectedUpdatedAt)
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if got.kind != tc.want.kind || !got.updatedAt.Equal(tc.want.updatedAt) {
			t.Fatalf("%s: got %+v, want %+v", tc.body, got, tc.want)
		}
	}
}

func TestBodyPreconditionRefusesWhatCannotBeAStoredValue(t *testing.T) {
	for _, raw := range []string{
		`12345`,
		`"yesterday"`,
		`""`,
		// Nanoseconds: the column stores microseconds, so this is not a value the
		// API returned, and truncating it would let a DIFFERENT value match.
		`"2026-09-23T16:10:44.123456789Z"`,
	} {
		var req branchRequest
		if err := json.Unmarshal([]byte(`{"expected_updated_at":`+raw+`}`), &req); err != nil {
			t.Fatalf("%s: decode: %v", raw, err)
		}
		if _, err := bodyPrecondition(req.ExpectedUpdatedAt); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
}

func TestQueryPrecondition(t *testing.T) {
	at := time.Date(2026, 9, 23, 16, 10, 44, 123456000, time.UTC)
	req := func(query string) *http.Request {
		return httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/campaigns/x/steps/y/branch"+query, http.NoBody)
	}
	if got, err := queryPrecondition(req("")); err != nil || got != (BranchPrecondition{}) {
		t.Fatalf("absent: %+v, %v", got, err)
	}
	got, err := queryPrecondition(req("?expected_updated_at=" + url.QueryEscape("2026-09-23T16:10:44.123456Z")))
	if err != nil || got.kind != preconditionAt || !got.updatedAt.Equal(at) {
		t.Fatalf("timestamp: %+v, %v", got, err)
	}
	// An offset's '+' must be escaped; unescaped it decodes to a space and the
	// value is refused rather than misread.
	for _, q := range []string{"?expected_updated_at=", "?expected_updated_at=nope", "?expected_updated_at=2026-09-23T18:10:44+02:00"} {
		if _, err := queryPrecondition(req(q)); err == nil {
			t.Errorf("%q accepted", q)
		}
	}
}

// updated_at is the token a client echoes back, so what the API serializes must
// parse to exactly the stored instant — including microseconds that end in
// zeros (RFC3339Nano trims them) and a whole second (no fraction at all).
func TestUpdatedAtRoundTripsExactly(t *testing.T) {
	for _, nanos := range []int{0, 100000000, 123456000, 1000, 999999000} {
		stored := time.Date(2026, 9, 23, 16, 10, 44, nanos, time.FixedZone("x", 3600))
		resp := toBranchResponse(gen.SequenceStepBranch{
			StepID: uuid.New(), Condition: "always", UpdatedAt: pgtype.Timestamptz{Time: stored, Valid: true},
		})
		back, err := parseExpectedUpdatedAt(resp.UpdatedAt)
		if err != nil {
			t.Fatalf("%q does not parse back: %v", resp.UpdatedAt, err)
		}
		if !back.Equal(stored) {
			t.Fatalf("%q round-trips to %v, want %v", resp.UpdatedAt, back, stored)
		}
	}
}

// A refused precondition is 409 with the branch that won; when the step has no
// branch any more, current is an explicit null, not a missing key.
func TestWriteGraphErrorBranchChanged(t *testing.T) {
	step := uuid.New()
	row := gen.SequenceStepBranch{StepID: step, Condition: "always", UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	cases := []struct {
		name    string
		err     error
		current bool
	}{
		{"replaced", &BranchChangedError{Current: &row}, true},
		{"removed", fmt.Errorf("tx: %w", &BranchChangedError{}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeGraphError(rec, tc.err, "fallback")
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body["code"]) != `"branch_changed"` || len(body["error"]) < 3 {
				t.Fatalf("body = %s", rec.Body.String())
			}
			raw, present := body["current"]
			if !present {
				t.Fatalf("current must always be present: %s", rec.Body.String())
			}
			if tc.current != (string(raw) != "null") || (tc.current && !strings.Contains(string(raw), step.String())) {
				t.Fatalf("current = %s", raw)
			}
		})
	}
}
