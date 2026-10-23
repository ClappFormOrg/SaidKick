package models

import (
	"cmp"
	"time"

	"github.com/google/uuid"
)

// Cluster is a Kubernetes target cluster registered with the platform via
// the sAId-Kick agent. Scope determines access for deployers: public clusters
// are open to every org; org-scoped clusters belong to one org; restricted
// clusters are open to the owning org plus each org with an explicit
// ClusterAccessGrant.
//
// Trust model: agent-pull only — the platform never stores a long-lived
// kubeconfig. AgentCertSerial + AgentCertExpiresAt track the mTLS cert
// issued during handshake; rotation every 90 days.
type Cluster struct {
	IDModel

	Name        string  `gorm:"type:varchar(80);not null" json:"name"`
	Description *string `gorm:"type:text"                 json:"description,omitempty"`

	// OrchestratorClusterName is the name this cluster is registered under
	// in the orchestrator (e.g. the Argo CD cluster name from `argocd cluster
	// list`). Defaults to Name when left blank at registration. Always use
	// EffectiveOrchestratorName() when building an ApplicationSpec.
	OrchestratorClusterName string `gorm:"type:varchar(80);not null;default:''" json:"orchestrator_cluster_name,omitempty"`

	// Scope is one of ClusterScopePublic, ClusterScopeOrg, or
	// ClusterScopeRestricted. The (scope, owning_organization_id) invariant is
	// enforced via CHECK constraint in migrate.go.
	Scope                ClusterScope  `gorm:"type:varchar(32);not null;index" json:"scope"`
	OwningOrganizationID *uuid.UUID    `gorm:"type:uuid;index"                 json:"owning_organization_id,omitempty"`
	OwningOrganization   *Organization `json:"-"`

	// VaultRoleID and VaultSecretID are the AppRole credentials for this
	// cluster's scoped Vault policy (kv/data/deployments/<cluster_id>/*).
	// Set by an operator after cluster registration. When blank the API
	// falls back to the global VAULT_TOKEN.
	VaultRoleID   *string `gorm:"type:varchar(200)" json:"-"`
	VaultSecretID *string `gorm:"type:varchar(200)" json:"-"`

	// ArgoCDProjectName is the Argo CD AppProject this cluster's
	// deployments should be placed in. Defaults to "default" when blank.
	ArgoCDProjectName *string `gorm:"column:argocd_project_name;type:varchar(80)" json:"argocd_project_name,omitempty"`

	// IngressClassName is the Kubernetes IngressClass name used on this cluster
	// (e.g. "nginx", "traefik", "haproxy"). Injected as a default value into
	// the deploy form so deployers do not have to look it up themselves.
	IngressClassName *string `gorm:"column:ingress_class_name;type:varchar(80)" json:"ingress_class_name,omitempty"`

	// BaseDomain is the cluster's wildcard base domain (e.g. "apps.example.nl").
	// The deploy form suggests <app-slug>.<base-domain> as the default hostname
	// for ingress resources so deployers only need to confirm or override it.
	BaseDomain *string `gorm:"column:base_domain;type:varchar(253)" json:"base_domain,omitempty"`

	AgentVersion       *string    `gorm:"type:varchar(64)" json:"agent_version,omitempty"`
	AgentCertSerial    *string    `gorm:"type:varchar(128)" json:"agent_cert_serial,omitempty"`
	AgentCertExpiresAt *time.Time `                        json:"agent_cert_expires_at,omitempty"`

	// AgentCertPendingSerial is the decimal serial of a renewed certificate
	// the agent has not presented yet. The agent's first request with it
	// promotes it to AgentCertSerial; until then the current certificate
	// keeps working (activation on first use, issue #139).
	AgentCertPendingSerial *string `gorm:"type:text" json:"-"`
	// AgentCertNotAfter is the expiry of the active certificate, set at
	// handshake and when a renewed certificate is promoted. NULL for
	// clusters that have neither handshaken nor renewed since it was added.
	AgentCertNotAfter *time.Time `gorm:"type:timestamptz" json:"agent_cert_not_after,omitempty"`
	// AgentCertRotationRequestedAt is set by rotate-credential and cleared
	// when a renewed certificate is issued. While set, heartbeat responses
	// tell the agent to renew.
	AgentCertRotationRequestedAt *time.Time `gorm:"type:timestamptz" json:"agent_cert_rotation_requested_at,omitempty"`

	State ClusterState `gorm:"type:varchar(32);not null;default:'pending_registration';index;check:state IN ('pending_registration','ready','ready_no_eso','unhealthy','decommissioned')" json:"state"`

	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at,omitempty"`
	RegisteredAt     *time.Time `json:"registered_at,omitempty"`
	DecommissionedAt *time.Time `json:"decommissioned_at,omitempty"`
}

// EffectiveOrchestratorName returns the name used when referring to this
// cluster inside the orchestrator. Falls back to Name when
// OrchestratorClusterName was not set at registration.
func (c Cluster) EffectiveOrchestratorName() string {
	return cmp.Or(c.OrchestratorClusterName, c.Name)
}

// EffectiveArgocdProject returns the Argo CD AppProject name for this cluster.
// Falls back to "default" when ArgoCDProjectName is not set.
func (c Cluster) EffectiveArgocdProject() string {
	if c.ArgoCDProjectName != nil && *c.ArgoCDProjectName != "" {
		return *c.ArgoCDProjectName
	}
	return "default"
}
