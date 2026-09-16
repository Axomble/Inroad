package coreapi

// WebhookDeliveryJob is everything the webhook:deliver worker needs to POST one
// delivery: the receiver URL, the exact JSON body that was stored, the DECRYPTED
// signing secret, and the attempt bookkeeping.
//
// The secret is opened control-plane-side (inprocess, via the keyring) and
// crosses this seam as plaintext bytes, exactly like SenderTransport.SMTPPassword
// — the worker never touches the keyring (docs/security.md invariant 1's
// execution-plane rule). Secret is []byte so the worker can zeroize it after
// signing.
//
// Resolving this is NOT a coreapi.Client method: it is consumed through the
// narrow, consumer-defined internal/worker/webhook.Core interface, satisfied by
// the in-process client via type assertion — the same "avoid widening Client's
// ~40-method surface (and its 13 fakes) for one call site" trade as
// BreakerResult / SenderTransport.
type WebhookDeliveryJob struct {
	DeliveryID string `json:"delivery_id"`
	// EndpointID names the endpoint whose signing secret this job needs. A fleet
	// worker brokers that secret by this id (credbroker.OpenWebhookEndpointSecret).
	EndpointID  string `json:"endpoint_id"`
	WorkspaceID string `json:"workspace_id"`
	EventType   string `json:"event_type"`
	// Payload is the stored, byte-identical body to POST (and re-sign each
	// attempt). It is NOT a secret — it is the body the receiver is about to be
	// handed — so it crosses the wire with the rest of the job.
	Payload []byte `json:"payload"`
	// Secret is the decrypted HMAC signing key. json:"-": it does not cross the
	// remote coreapi wire; see the package doc on coreapi.
	Secret []byte `json:"-"`
	// URL is the receiver endpoint; the worker re-runs the SSRF guard on it
	// immediately before dialing (the DNS-rebinding window).
	URL string `json:"url"`
	// Attempts is how many attempts have already been made (0 before the first).
	Attempts int `json:"attempts"`
	// Status is the delivery row's current status. The handler no-ops on anything
	// but "pending".
	Status string `json:"status"`
	// EndpointActive is false when the endpoint was deactivated after this
	// delivery was queued — the handler fails the delivery without dialing.
	EndpointActive bool `json:"endpoint_active"`
}
