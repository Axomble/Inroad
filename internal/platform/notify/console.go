package notify

import "context"

// consoleSender renders the message via an injected sink (logger in prod,
// capture func in tests). No network. Dev/test default.
type consoleSender struct{ sink func(Message) }

func (c *consoleSender) Send(_ context.Context, m Message) error {
	c.sink(m)
	return nil
}
