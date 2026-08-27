package queue

import (
	"bytes"
	"os"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// TestQueueDepthOfMapsEveryBacklogState pins the asynq→metrics field mapping.
// Every value is distinct so a transposition (the realistic bug: Retry and
// Scheduled swapped, or Archived dropped) fails rather than coincidentally
// matching.
func TestQueueDepthOfMapsEveryBacklogState(t *testing.T) {
	got := queueDepthOf(&asynq.QueueInfo{
		Queue:     "w:node-1",
		Pending:   1,
		Active:    2,
		Scheduled: 3,
		Retry:     4,
		Archived:  5,
		// Fields metrics deliberately does NOT export: they describe storage
		// and history, not whether workers are keeping up.
		Completed:      6,
		Aggregating:    7,
		ProcessedTotal: 8,
	})
	want := metrics.QueueDepth{
		Name: "w:node-1", Pending: 1, Active: 2, Scheduled: 3, Retry: 4, Dead: 5,
	}
	if got != want {
		t.Fatalf("queueDepthOf = %+v, want %+v", got, want)
	}
}

// TestQueueDepthOfEmptyQueueIsAllZeros: an idle queue must map to explicit
// zeros, not to absent values — an alert needs to tell "empty" from "missing".
func TestQueueDepthOfEmptyQueueIsAllZeros(t *testing.T) {
	got := queueDepthOf(&asynq.QueueInfo{Queue: "default"})
	if want := (metrics.QueueDepth{Name: "default"}); got != want {
		t.Fatalf("queueDepthOf = %+v, want %+v", got, want)
	}
}

// TestInspectorSatisfiesTheMetricsSeam is a compile-time contract check: the
// adapter exists solely so platform/metrics need not import asynq, and that
// only holds if *Inspector actually implements metrics.QueueInspector.
func TestInspectorSatisfiesTheMetricsSeam(t *testing.T) {
	var _ metrics.QueueInspector = (*Inspector)(nil)
}

// TestInspectorIsReadOnly enforces the restraint the type's doc claims. The
// deferred-send design rests on "undo is a row status flip the handler
// re-reads", never a queue mutation; now that an asynq.Inspector exists in this
// codebase, the tempting shortcut (delete/archive/cancel the task instead) is
// one method away. This fails the moment someone adds it.
func TestInspectorIsReadOnly(t *testing.T) {
	src, err := os.ReadFile("inspect.go")
	if err != nil {
		t.Fatalf("read inspect.go: %v", err)
	}
	for _, mutator := range []string{
		"DeleteTask", "DeleteQueue", "DeleteAll", "ArchiveTask", "ArchiveAll",
		"CancelProcessing", "RunTask", "RunAll", "PauseQueue", "UnpauseQueue",
	} {
		if bytes.Contains(src, []byte(mutator)) {
			t.Errorf("inspect.go calls asynq's %s; this Inspector must stay read-only "+
				"(cancellation is a DB status flip, not a queue mutation)", mutator)
		}
	}
}
