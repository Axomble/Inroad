package credbroker

// The wire contract between a worker (client) and the control plane (server).
// Both sides are in this package on purpose: one file defines the shapes, so
// the two transports cannot drift into disagreeing about a field name.
//
// Secret-bearing fields are []byte, which encoding/json marshals as base64 and
// decodes back to bytes — no hand-rolled encoding, and a nil stays null rather
// than becoming an empty-but-present secret.

// The broker's two routes. Mounted under the caller-supplied base URL by the
// client and at the router root by Handler.
const (
	PathMailbox         = "/internal/fleet/credentials/mailbox"
	PathWebhookEndpoint = "/internal/fleet/credentials/webhook-endpoint"
)

// mailboxRequest names the mailbox whose credential is wanted. It carries IDS
// ONLY — deliberately no ciphertext, no provider, no host. A caller that could
// supply the ciphertext would be choosing what gets decrypted, which would make
// the broker a general decryption oracle and defeat its purpose; the control
// plane re-reads the row itself, workspace-pinned.
//
// WorkerID is the caller's claimed fleet identity, not itself a credential —
// the bearer token is what authenticates the request. It only narrows what an
// otherwise-valid token can open; see MailboxRef.WorkerID and localOpener.
type mailboxRequest struct {
	WorkspaceID string `json:"workspace_id"`
	MailboxID   string `json:"mailbox_id"`
	WorkerID    string `json:"worker_id,omitempty"`
}

// mailboxResponse is one opened transport credential. Exactly one secret field
// is populated, selected by Provider.
type mailboxResponse struct {
	Provider     string `json:"provider"`
	AccessToken  []byte `json:"access_token"`
	SMTPPassword []byte `json:"smtp_password"`
}

// webhookEndpointRequest names the endpoint whose signing secret is wanted.
// Ids only, for the same reason as mailboxRequest.
type webhookEndpointRequest struct {
	WorkspaceID string `json:"workspace_id"`
	EndpointID  string `json:"endpoint_id"`
}

// webhookEndpointResponse carries the endpoint's HMAC signing secret.
type webhookEndpointResponse struct {
	Secret []byte `json:"secret"`
}

// errorResponse is the only body an error ever returns. The message is a fixed
// string chosen by the handler, never an upstream or database error text: a
// broker that echoed "no rows in result set" or a pg error would be a probe
// oracle for which mailbox ids exist.
type errorResponse struct {
	Error string `json:"error"`
}
