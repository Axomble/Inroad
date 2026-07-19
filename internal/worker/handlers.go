// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/warmup"
)

// Register attaches all execution-plane handlers to the mux.
func Register(mux *asynq.ServeMux, core coreapi.Client) {
	mux.HandleFunc(queue.TaskWarmupTick, warmup.Handler(core))
}
