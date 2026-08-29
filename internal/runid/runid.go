// Package runid carries the identifier of one shopping run through a request.
// Every trail row written while it is in scope gets the same id, so a run can
// be read back as one story instead of unrelated rows that happen to be near
// each other in time.
package runid

import (
	"context"

	"github.com/google/uuid"
)

type contextKey struct{}

// New returns an identifier for a fresh run.
func New() string { return uuid.NewString() }

// With returns a context that marks work as belonging to a run.
func With(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, id)
}

// From returns the run this work belongs to, or an empty string outside a run.
func From(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
