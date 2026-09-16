package remote

// The wire contract between a worker (Client) and the control plane (Handler).
// Both sides are in this package on purpose, exactly as credbroker does it: one
// file defines the shapes, so the two transports cannot drift into disagreeing
// about a field name.

const (
	// PathPrefix is where cmd/inroad mounts NewHandler on the fleet listener,
	// beside credbroker.PathPrefix. Keeping every coreapi route under one
	// prefix is what lets the composition root mount this package as a unit
	// without naming its individual routes.
	PathPrefix = "/internal/fleet/coreapi/"
	// PathSuppressionCheck answers one suppression question. It is the ONLY
	// route in slice 1 of this transport.
	PathSuppressionCheck = PathPrefix + "suppression/check"
)

// suppressionRequest asks whether ONE named address is suppressed in ONE named
// workspace. It carries a workspace id and a single address — deliberately no
// filter, no pattern, no limit and no cursor.
//
// That restriction is the same one credbroker's ids-only request enforces, for
// the same reason: a request that could express "rows matching X" would make
// this seam a general query engine over the tenant database, which is precisely
// the capability the plane split exists to take away. Every route added in a
// later slice has to answer to that rule too.
//
// The address itself is not an id, and that is worth stating rather than
// glossing. The subject of a suppression record IS an email address, and the
// worker is about to send to this one — it already holds it, from the job it
// was handed. So the request reveals nothing to the control plane and the
// ANSWER reveals one bit about an address the caller named. What it is not is
// enumerable: there is no way to list the suppression table, page it, or match
// a prefix, so learning the list means already knowing every address in it.
type suppressionRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
}

// suppressionResponse is the one bit of the answer. A dedicated struct rather
// than a bare boolean so the route can gain a field (a reason, an as-of time)
// without every deployed worker needing to change on the same day.
type suppressionResponse struct {
	Suppressed bool `json:"suppressed"`
}

// errorResponse is the only body an error ever returns. The message is a FIXED
// string chosen by the handler, never an upstream or database error text: a
// seam that echoed "no rows in result set" or a pg error would be a probe
// oracle, and one that echoed a query plan would be worse.
type errorResponse struct {
	Error string `json:"error"`
}
