// Package principalctx owns the context key that carries the authenticated
// principal. Package auth stores and reads the principal through it, and
// package authtest uses it to attach a principal in tests. It does not import
// auth, so the value is untyped here; auth type-asserts it to *auth.Principal.
package principalctx

import "context"

type key struct{}

// With returns a copy of ctx carrying p.
func With(ctx context.Context, p any) context.Context {
	return context.WithValue(ctx, key{}, p)
}

// From returns the value stored by With, or nil.
func From(ctx context.Context) any {
	return ctx.Value(key{})
}
