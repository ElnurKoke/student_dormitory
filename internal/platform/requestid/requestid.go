// Package requestid carries an opaque operation ID without depending on HTTP.
package requestid

import (
	"context"
	"crypto/rand"
)

type contextKey struct{}

func New() string { return rand.Text() }

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
