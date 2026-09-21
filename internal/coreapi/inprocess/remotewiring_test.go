package inprocess

import (
	"github.com/inroad/inroad/internal/coreapi/remote"
)

// The slice-4 half of the guard jobsource_test.go describes.
//
// cmd/inroad reaches these two capabilities by TYPE ASSERTION on the in-process
// client, so a signature that drifts does not break a build — it makes the
// assertion fail and the fleet listener refuse to start with a message naming
// an interface, which is a startup failure rather than a compile failure and
// costs an operator a deploy to discover.
//
// These are not paired with an execution-plane *Source field the way JobSource
// and OutcomeSource are, and that is the shape slice 4 changed. A role=send
// worker no longer builds an in-process client at ALL — it uses
// *remote.Client directly, which satisfies coreapi.Client whole — so there is
// nothing here for a fleet worker to swap out. The control plane's SERVING side
// is the only side left, and this is its guard.
var (
	_ remote.InboundWriter = client{}
	_ remote.FleetWriter   = client{}
)
