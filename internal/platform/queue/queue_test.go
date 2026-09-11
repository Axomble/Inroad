package queue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

// fakeEnqueuer is a Redis-free stand-in for Client.inner. It records the task
// and options a producer asked for, mirroring the fakeEnqueuer/optByType
// pattern already established in redisbus_test.go — asynq.Option is a public
// Type()/Value() pair, so a fake can assert on it directly without executing a
// real enqueue.
type fakeEnqueuer struct {
	task *asynq.Task
	opts []asynq.Option
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, t *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	f.task = t
	f.opts = opts
	return &asynq.TaskInfo{}, nil
}

func (f *fakeEnqueuer) Close() error { return nil }

// queue returns the asynq.Queue option's value from the last EnqueueContext
// call, and whether one was present at all. Delegates to queueOption, the same
// scan production code's enqueue guard uses.
func (f *fakeEnqueuer) queue() (string, bool) { return queueOption(f.opts) }

// TestEveryProducerTargetsARoleQueue proves every non-affinity enqueue helper
// sets an explicit queue. A task with no queue lands on asynq's "default",
// which after the role split nothing new should use — and which the control
// role deliberately does not consume. A missed Queue option here is a task
// that only drains while the transitional default consumption survives.
func TestEveryProducerTargetsARoleQueue(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Client) error
		want string
	}{
		{"warmup engage", func(c *Client) error { return c.EnqueueWarmupEngageIn("r1", "ws1", time.Minute) }, QueueSend},
		{"advance", func(c *Client) error { return c.EnqueueAdvance("e1", "ws1") }, QueueSend},
		{"advance at", func(c *Client) error { return c.EnqueueAdvanceAt("e1", "ws1", time.Now()) }, QueueSend},
		{"advance in", func(c *Client) error { return c.EnqueueAdvanceIn("e1", "ws1", time.Minute) }, QueueSend},
		{"test send", func(c *Client) error { return c.EnqueueTestSend("c1", "s1", "m1", "to@x.test", "ws1") }, QueueSend},
		{"pending reply", func(c *Client) error { return c.EnqueuePendingInboxReply("p1", "ws1", time.Now()) }, QueueSend},
		{"pending compose", func(c *Client) error { return c.EnqueuePendingInboxCompose("p1", "ws1", time.Now()) }, QueueSend},
		{"inbox poll", func(c *Client) error { return c.EnqueueInboxPoll("m1", "ws1") }, QueueSend},
		{"webhook deliver", func(c *Client) error { return c.EnqueueWebhookDeliver("d1", "ws1") }, QueueSend},
		{"webhook deliver in", func(c *Client) error { return c.EnqueueWebhookDeliverIn("d1", "ws1", time.Minute) }, QueueSend},
		{"deliverability evaluate", func(c *Client) error { return c.EnqueueDeliverabilityEvaluate("c1", "ws1") }, QueueControl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEnqueuer{}
			c := &Client{inner: fake}
			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			got, ok := fake.queue()
			if !ok || got != tc.want {
				t.Errorf("%s enqueued to %q (present=%v), want %q", tc.name, got, ok, tc.want)
			}
		})
	}
}

// TestQueueForTaskTypeIsTheOneRoutingTable proves the lookup answers for every
// task type a producer can enqueue, and refuses everything else.
//
// The refusals are the point. An unmapped type must come back not-ok rather
// than "" — "" means asynq's own "default" queue downstream, which is the
// silent misroute the table exists to prevent — and inbox:reply_send must stay
// out of it: nothing enqueues that type any more and Replay refuses it by task
// type, so a mapping would assert a route nothing may take.
func TestQueueForTaskTypeIsTheOneRoutingTable(t *testing.T) {
	for taskType, want := range map[string]string{
		TaskWarmupTick:              QueueSend,
		TaskWarmupEngage:            QueueSend,
		TaskSequenceAdvance:         QueueSend,
		TaskInboxPoll:               QueueSend,
		TaskInboxPendingReplySend:   QueueSend,
		TaskInboxPendingComposeSend: QueueSend,
		TaskTestSend:                QueueSend,
		TaskWebhookDeliver:          QueueSend,
		TaskWarmupSweep:             QueueControl,
		TaskInboxSweep:              QueueControl,
		TaskSweepEnrollments:        QueueControl,
		TaskMaintenanceCleanup:      QueueControl,
		TaskDomainAuthSweep:         QueueControl,
		TaskRecipientESPSweep:       QueueControl,
		TaskDeliverabilityEvaluate:  QueueControl,
	} {
		got, ok := queueForTaskType(taskType)
		if !ok || got != want {
			t.Errorf("queueForTaskType(%q) = (%q, %v), want (%q, true)", taskType, got, ok, want)
		}
	}

	for _, taskType := range []string{"", "some:unknown-task", TaskInboxReplySend} {
		if got, ok := queueForTaskType(taskType); ok {
			t.Errorf("queueForTaskType(%q) = (%q, true), want not routable", taskType, got)
		}
	}
}

// TestEnqueueRefusesATaskWithNoQueueOption proves the funnel every raw-asynq
// producer passes through (c.enqueue) rejects a task carrying no asynq.Queue
// option, rather than letting it silently land on asynq's unconsumed
// "default". Nothing today calls enqueue without one (see
// TestEveryProducerTargetsARoleQueue), but nothing stopped a FUTURE producer
// from forgetting it either — and a forgotten queue drains only while the
// transitional default consumption survives, then quietly stops. The error
// names the task type so the offending producer is identifiable from the
// message alone.
func TestEnqueueRefusesATaskWithNoQueueOption(t *testing.T) {
	fake := &fakeEnqueuer{}
	c := &Client{inner: fake}

	err := c.enqueue(asynq.NewTask("some:unrouted-task", nil))
	if err == nil {
		t.Fatal("enqueue with no Queue option: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "some:unrouted-task") {
		t.Errorf("error %q does not name the task type", err.Error())
	}
	if fake.task != nil {
		t.Error("enqueue must reject before reaching the underlying client, not after")
	}

	// A task that DOES carry a Queue option still enqueues normally.
	fake = &fakeEnqueuer{}
	c = &Client{inner: fake}
	if err := c.enqueue(asynq.NewTask("some:routed-task", nil), asynq.Queue(QueueSend)); err != nil {
		t.Fatalf("enqueue with a Queue option: %v", err)
	}
	if fake.task == nil {
		t.Error("a task with a Queue option must still reach the underlying client")
	}
}

// malformedQueueOption implements asynq.Option and reports QueueOpt, like the
// real asynq.Queue(...), but backs Value() with an int instead of a string —
// standing in for a hypothetical future asynq release that changes QueueOpt's
// value type. It exists to prove queueOption uses the comma-ok form rather
// than a bare o.Value().(string): the latter would panic here, on the send
// path (queueOption is called from c.enqueue), turning a routing check into a
// production panic at enqueue time.
type malformedQueueOption struct{}

func (malformedQueueOption) String() string         { return "Queue(malformed)" }
func (malformedQueueOption) Type() asynq.OptionType { return asynq.QueueOpt }
func (malformedQueueOption) Value() interface{}     { return 42 }

// TestQueueOptionIgnoresANonStringValueRatherThanPanicking proves queueOption
// treats a QueueOpt whose Value() is not a string as no usable option found —
// it keeps scanning rather than panicking, and c.enqueue then rejects the
// task exactly as it would an entirely missing Queue option (fail the
// enqueue, never silently accept a malformed one, never crash the process).
func TestQueueOptionIgnoresANonStringValueRatherThanPanicking(t *testing.T) {
	if got, ok := queueOption([]asynq.Option{malformedQueueOption{}}); ok || got != "" {
		t.Fatalf("queueOption(malformed) = (%q, %v), want (\"\", false)", got, ok)
	}

	fake := &fakeEnqueuer{}
	c := &Client{inner: fake}
	err := c.enqueue(asynq.NewTask("some:malformed-queue-task", nil), malformedQueueOption{})
	if err == nil {
		t.Fatal("enqueue with a malformed Queue option: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "some:malformed-queue-task") {
		t.Errorf("error %q does not name the task type", err.Error())
	}
	if fake.task != nil {
		t.Error("enqueue must reject before reaching the underlying client, not after")
	}
}

// TestWarmupTickKeepsItsPerWorkerAffinityAndFallsBackToSend proves affinity is
// load-bearing: warmup reputation is per-IP, so a warming mailbox must keep
// sending from the same worker. An unassigned tick still needs SOME queue the
// send role actually drains, so it falls back to QueueSend rather than the
// unconsumed "default".
func TestWarmupTickKeepsItsPerWorkerAffinityAndFallsBackToSend(t *testing.T) {
	fake := &fakeEnqueuer{}
	c := &Client{inner: fake}

	if err := c.EnqueueWarmupTickAt("m1", "ws1", time.Now(), WorkerQueue("abc")); err != nil {
		t.Fatalf("with dest: %v", err)
	}
	if got, ok := fake.queue(); !ok || got != WorkerQueue("abc") {
		t.Errorf("dest = %q (present=%v), want the affinity queue", got, ok)
	}

	if err := c.EnqueueWarmupTickAt("m1", "ws1", time.Now(), ""); err != nil {
		t.Fatalf("no dest: %v", err)
	}
	if got, ok := fake.queue(); !ok || got != QueueSend {
		t.Errorf("unassigned tick went to %q (present=%v), want %q — default is not consumed by control and is transitional", got, ok, QueueSend)
	}
}

// fakeRegistrar records what a scheduler registration asked for. Register on a
// real *asynq.Scheduler only stores a cron entry locally — it does not touch
// Redis until the entry fires — but there is no way to read that entry back
// out from outside the asynq package, so exercising the routing decision needs
// this seam instead.
type fakeRegistrar struct {
	cronspec string
	task     *asynq.Task
	opts     []asynq.Option
}

func (f *fakeRegistrar) Register(cronspec string, task *asynq.Task, opts ...asynq.Option) (string, error) {
	f.cronspec = cronspec
	f.task = task
	f.opts = opts
	return "entry-1", nil
}

// queue delegates to queueOption, the same scan fakeEnqueuer.queue() and
// production code's enqueue guard use.
func (f *fakeRegistrar) queue() (string, bool) { return queueOption(f.opts) }

// TestEveryControlSweepTargetsControlQueue proves every periodic reconcile —
// fan-outs and cross-tenant scans — registers on QueueControl, so a send-role
// host (which does not consume it, per internal/worker/queues.go) can never
// claim one and silently stop sending behind it.
func TestEveryControlSweepTargetsControlQueue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cronspec string
		taskType string
	}{
		{"sweep enrollments", "@every 5m", TaskSweepEnrollments},
		{"inbox sweep", "@every " + inboxSweepInterval.String(), TaskInboxSweep},
		{"warmup sweep", "@every 5m", TaskWarmupSweep},
		{"maintenance cleanup", "@every 24h", TaskMaintenanceCleanup},
		{"domain auth sweep", "@every 1h", TaskDomainAuthSweep},
		{"recipient esp sweep", "@every 5m", TaskRecipientESPSweep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRegistrar{}
			if err := registerControlSweep(fake, tc.cronspec, tc.taskType); err != nil {
				t.Fatalf("registerControlSweep: %v", err)
			}
			if fake.cronspec != tc.cronspec {
				t.Errorf("cronspec = %q, want %q", fake.cronspec, tc.cronspec)
			}
			if fake.task.Type() != tc.taskType {
				t.Errorf("task type = %q, want %q", fake.task.Type(), tc.taskType)
			}
			if got, ok := fake.queue(); !ok || got != QueueControl {
				t.Errorf("queue = %q (present=%v), want %q", got, ok, QueueControl)
			}
		})
	}
}

func TestWarmupTickPayloadRoundTrip(t *testing.T) {
	p := WarmupTickPayload{MailboxID: "mb-123", WorkspaceID: "ws-1"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WarmupTickPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-123" || got.WorkspaceID != "ws-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskWarmupTick != "warmup:tick" || TaskWarmupSweep != "warmup:sweep" {
		t.Errorf("task name drift: %q %q", TaskWarmupTick, TaskWarmupSweep)
	}
}

// TestWarmupTickTaskID proves the dedup key collapses two ticks whose due times
// fall in the same whole second to one key (a sweep re-seed racing the lazy
// chain), while a later second yields a distinct key (a genuinely new tick).
func TestWarmupTickTaskID(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	a := warmupTickTaskID("mb-1", base)
	b := warmupTickTaskID("mb-1", base.Add(500*time.Millisecond))
	c := warmupTickTaskID("mb-1", base.Add(time.Second))
	if a != "warmup:mb-1:1700000000" {
		t.Fatalf("task id = %q, want warmup:mb-1:1700000000", a)
	}
	if a != b {
		t.Fatalf("sub-second ticks must share a key: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("a later-second tick must get a distinct key: %q == %q", a, c)
	}
	if warmupTickTaskID("mb-2", base) == a {
		t.Fatalf("different mailboxes must get distinct keys")
	}
}

func TestAdvancePayloadRoundTrip(t *testing.T) {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: "e1", WorkspaceID: "w1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AdvancePayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EnrollmentID != "e1" || got.WorkspaceID != "w1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskSequenceAdvance != "sequence:advance" || TaskSweepEnrollments != "sequence:sweep_stuck_enrollments" {
		t.Errorf("task name drift: %q %q", TaskSequenceAdvance, TaskSweepEnrollments)
	}
}

func TestInboxPollPayloadRoundTrip(t *testing.T) {
	b, err := json.Marshal(InboxPollPayload{MailboxID: "mb-1", WorkspaceID: "w1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got InboxPollPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-1" || got.WorkspaceID != "w1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskInboxSweep != "inbox:sweep" || TaskInboxPoll != "inbox:poll" {
		t.Errorf("task name drift: %q %q", TaskInboxSweep, TaskInboxPoll)
	}
}

// TestInboxPollTaskID proves the dedup key collapses every replica's sweep within
// one interval to a single poll, while a later interval still gets its own key.
//
// Both halves matter. Without the first, N replicas each open a real IMAP
// connection per mailbox per interval — a provider rate-limit problem for zero
// gain, since the extra polls read the same messages. Without the second, a
// mailbox that has been polled once would never be polled again.
func TestInboxPollTaskID(t *testing.T) {
	// A bucket boundary, so the truncation is exercised rather than accidentally
	// landing mid-interval.
	base := time.Unix(1_700_000_000, 0).Truncate(inboxSweepInterval)
	a := inboxPollTaskID("mb-1", base)

	// Every instant inside the interval — start, middle, and the last moment before
	// the next bucket — shares one key.
	for _, offset := range []time.Duration{0, inboxSweepInterval / 2, inboxSweepInterval - time.Nanosecond} {
		if got := inboxPollTaskID("mb-1", base.Add(offset)); got != a {
			t.Fatalf("offset %s must share the bucket key: %q != %q", offset, got, a)
		}
	}
	// The next interval is a genuinely new poll, so the dedup window is bounded.
	if got := inboxPollTaskID("mb-1", base.Add(inboxSweepInterval)); got == a {
		t.Fatalf("the next interval must get a distinct key: %q == %q", got, a)
	}
	// Different mailboxes never suppress each other.
	if inboxPollTaskID("mb-2", base) == a {
		t.Fatal("different mailboxes must get distinct keys")
	}
	// The key is namespaced, so it cannot collide with warmup/testsend/reply keys
	// in asynq's shared task-id space.
	if !strings.HasPrefix(a, "inbox-poll:mb-1:") {
		t.Fatalf("task id = %q, want an inbox-poll:mb-1: prefix", a)
	}
}

func TestTestSendPayloadRoundTrip(t *testing.T) {
	p := TestSendPayload{CampaignID: "c1", StepID: "s1", MailboxID: "m1", To: "preview@example.com", WorkspaceID: "w1"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got TestSendPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, p)
	}
	if TaskTestSend != "testsend:send" {
		t.Errorf("task name drift: %q", TaskTestSend)
	}
}

// The DRAIN shape. Nothing enqueues an inbox:reply_send any more, but tasks
// already in Redis still carry this payload and its JSON keys, so the wire shape
// and the task name are frozen until the drain handler is deleted with them —
// changing either would silently strand every reply in flight at deploy time.
func TestInboxReplySendPayloadRoundTrip(t *testing.T) {
	p := InboxReplySendPayload{ThreadID: "t1", BodyText: "thanks!", WorkspaceID: "w1", TaskID: "inboxreply:t1:1700000000"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got InboxReplySendPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, p)
	}
	if TaskInboxReplySend != "inbox:reply_send" {
		t.Errorf("task name drift: %q", TaskInboxReplySend)
	}
}

// TestTestSendTaskID proves the dedup key collapses two enqueues for the SAME
// (campaign, step, mailbox) within the same second (a double-submitted form),
// while a later second yields a distinct key (a genuinely new test-send); a
// different mailbox always yields a distinct key.
func TestTestSendTaskID(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	a := testSendTaskID("c1", "s1", "m1", base)
	if a != "testsend:c1:s1:m1:1700000000" {
		t.Fatalf("task id = %q, want testsend:c1:s1:m1:1700000000", a)
	}
	if got := testSendTaskID("c1", "s1", "m1", base.Add(500*time.Millisecond)); got != a {
		t.Fatalf("sub-second re-submits must share a key: %q != %q", got, a)
	}
	if got := testSendTaskID("c1", "s1", "m1", base.Add(time.Second)); got == a {
		t.Fatalf("a later-second test-send must get a distinct key: %q == %q", got, a)
	}
	if testSendTaskID("c1", "s1", "m2", base) == a {
		t.Fatal("different mailboxes must get distinct keys")
	}
}

// TestQueuePriorities proves an ordered queue list maps to descending asynq
// weights (earlier = higher priority) with duplicates and empty names dropped,
// so a worker prefers its own per-IP queue over the shared default (spec §15)
// without starving it.
func TestQueuePriorities(t *testing.T) {
	got := queuePriorities([]string{"w:node-a", "default"})
	if got["w:node-a"] != 2 || got["default"] != 1 {
		t.Fatalf("weights = %v, want w:node-a=2 default=1", got)
	}

	// Duplicates and empty names are dropped, leaving one entry each; the earlier
	// queue keeps a strictly higher weight so it stays preferred.
	got = queuePriorities([]string{"w:node-a", "", "w:node-a", "default"})
	if len(got) != 2 {
		t.Fatalf("deduped map = %v, want 2 entries", got)
	}
	if got["w:node-a"] <= got["default"] || got["default"] < 1 {
		t.Fatalf("weights = %v, want w:node-a > default >= 1", got)
	}

	// An empty list yields an empty map so NewServer leaves asynq on its default.
	if m := queuePriorities(nil); len(m) != 0 {
		t.Fatalf("queuePriorities(nil) = %v, want empty", m)
	}
}
