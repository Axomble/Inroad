package coreapi

// Seam types for the recipient-domain ESP cache (recipientesp:sweep).
//
// The methods that carry them are deliberately NOT on Client, for the reason
// stated on BreakerResult: Client already carries a large method set that every
// test fake implements in full, so adding to it to serve one worker would break
// every fake for no gain in the seam's expressiveness. The sweep depends on a
// three-method interface it defines itself (recipientesp.Core), satisfied by the
// in-process client via type assertion at the composition root — the same shape
// as maintenance.Cleaner and deliverability.Breaker.

// RecipientDomainRef is one domain awaiting classification, paired with the
// workspace its write-back is pinned to. The sweep's fan-out is global (cache
// maintenance is infrastructure, not a tenant read), so the workspace has to
// travel with each row rather than being ambient.
type RecipientDomainRef struct {
	WorkspaceID string
	Domain      string
}

// RecipientDomainESP is one COMPLETED MX classification, ready to persist.
// A lookup that did not complete must never be expressed as one of these: the
// write stamps checked_at, which would hide the domain from the next sweep for a
// full staleness window on an answer that never arrived.
//
// MXHost is the primary MX as observed and is diagnostic only — nothing routes
// on it — but it is the only thing that explains to an operator why a domain was
// classified "other".
type RecipientDomainESP struct {
	WorkspaceID string
	Domain      string
	ESP         string
	MXHost      string
}
