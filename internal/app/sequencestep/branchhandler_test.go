package sequencestep

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

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
