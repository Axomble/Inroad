// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import "context"

type Client interface {
	// MailboxExists reports whether a mailbox is present and active.
	MailboxExists(ctx context.Context, id string) (bool, error)
}
