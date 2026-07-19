// Package inprocess is the v1 coreapi implementation: direct in-process access.
package inprocess

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

type client struct{}

// New returns the in-process coreapi client. It is intentionally a stub until
// the mailbox domain exists; the interface it satisfies will not change.
func New() coreapi.Client { return client{} }

func (client) MailboxExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}
