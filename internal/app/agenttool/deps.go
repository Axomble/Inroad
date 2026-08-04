package agenttool

// Deps carries the domain capabilities the tools call. Every field is one of
// this package's own narrow interfaces (declared beside the tools that use
// them), never a concrete domain service: the registry is unit-testable
// against fakes, and `app/*` packages still do not import each other — the
// composition root in cmd/inroad supplies the adapters, exactly as it already
// does for campaign.Checker and contact.ListChecker.
//
// A nil field means the deployment cannot serve that capability, and the
// registry simply does not register the tools that need it. That is what lets
// the surface grow one wiring at a time instead of failing closed on startup
// or, worse, offering a tool that panics when the model picks it.
type Deps struct {
	Campaigns      CampaignReader
	CampaignAdmin  CampaignController
	Contacts       ContactReader
	ContactWrites  ContactWriter
	Mailboxes      MailboxReader
	Lists          ListReader
	ListWrites     ListWriter
	Deliverability DeliverabilityReader
	Warmup         WarmupReader
}
