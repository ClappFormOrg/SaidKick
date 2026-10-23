package auth

import (
	"context"

	"github.com/google/uuid"
)

// AgentIdentity carries the cluster ID extracted from an inbound agent
// request. It is stored on the request context by AgentAuthMiddleware and
// read by agent-facing handlers.
type AgentIdentity struct {
	ClusterID uuid.UUID
}

const agentIdentityKey ctxKey = 1

// StoreAgentIdentity attaches an AgentIdentity to ctx.
func StoreAgentIdentity(ctx context.Context, id AgentIdentity) context.Context {
	return context.WithValue(ctx, agentIdentityKey, &id)
}

// AgentIdentityFromContext returns the AgentIdentity stored by the agent
// auth middleware, or nil if the request did not pass agent authentication.
func AgentIdentityFromContext(ctx context.Context) *AgentIdentity {
	v, _ := ctx.Value(agentIdentityKey).(*AgentIdentity)
	return v
}
