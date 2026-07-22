// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/inbox"
	"github.com/inroad/inroad/internal/worker/sender"
	"github.com/inroad/inroad/internal/worker/sequence"
	"github.com/inroad/inroad/internal/worker/warmup"
)

// Register attaches all execution-plane handlers to the mux.
func Register(mux *asynq.ServeMux, core coreapi.Client, sndr *mail.NetSender, reader mail.InboxReader, enq *queue.Client) {
	mux.HandleFunc(queue.TaskWarmupTick, warmup.Handler(core))
	mux.HandleFunc(queue.TaskSendEmail, sender.Handler(core, sndr, enq))
	mux.HandleFunc(queue.TaskSweepStuck, sender.SweepStuckHandler(core, enq))
	// Multi-step sequencing: advance one step per task (lazy chain) + reconcile.
	mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(core, sndr, enq))
	mux.HandleFunc(queue.TaskSweepEnrollments, sequence.SweepHandler(core, enq))
	// Reply & bounce detection: poll one mailbox's INBOX per task + reconcile.
	inbox.Register(mux, core, reader, enq)
}
