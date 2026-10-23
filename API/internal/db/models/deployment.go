package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Deployment is one instance of an AppVersion running on one Cluster in one
// Kubernetes namespace. Pinned to a specific git SHA so chart upgrades are
// explicit and never silent.
//
// Secrets supplied at deploy time are stored in the central Vault at
// kv/data/deployments/<deployment_id>/<field_name>. The SecretRefs field
// holds only the field-to-vault-path mapping — the values themselves never
// touch this table.
type Deployment struct {
	IDModel

	AppID uuid.UUID `gorm:"type:uuid;not null;index" json:"app_id"`
	App   App       `json:"-"`

	AppVersionID uuid.UUID  `gorm:"type:uuid;not null" json:"app_version_id"`
	AppVersion   AppVersion `json:"-"`

	ClusterID uuid.UUID `gorm:"type:uuid;not null;index" json:"cluster_id"`
	Cluster   Cluster   `json:"-"`

	OwningOrganizationID uuid.UUID    `gorm:"type:uuid;not null;index" json:"owning_organization_id"`
	OwningOrganization   Organization `json:"-"`

	DeployerUserID uuid.UUID `gorm:"type:uuid;not null" json:"deployer_user_id"`
	DeployerUser   User      `gorm:"foreignKey:DeployerUserID" json:"-"`

	// Namespace is the target Kubernetes namespace. Validated as a DNS-1123
	// label at application layer.
	Namespace string `gorm:"type:varchar(63);not null" json:"namespace"`

	// Values is the non-secret half of helm-values, validated against the
	// AppVersion's ValuesSchema at submit-time.
	Values datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"values"`

	// SecretRefs is an array of {field_path, vault_path} records — one per
	// secret-typed value in the chart's schema.
	SecretRefs datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"secret_refs"`

	State DeploymentState `gorm:"type:varchar(32);not null;default:'pending';index;check:state IN ('pending','waiting_for_cluster','applying','secret_sync','reconciling','ready','degraded','failed_render','failed_vault','failed_orchestrator','failed_secret_sync','pending_orchestrator','deleting','deleted')" json:"state"`

	LastStateChangeAt time.Time `gorm:"autoCreateTime" json:"last_state_change_at"`

	// LastArgocdAppName is the Argo CD Application CR name we created for
	// this deployment in the spoke's argocd namespace. Set after the CR is
	// composed and applied; nullable until then.
	LastArgocdAppName *string `gorm:"type:varchar(255)" json:"last_argocd_app_name,omitempty"`

	CreatedAt time.Time      `gorm:"autoCreateTime" json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"          json:"-"`
}

// BeforeUpdate ensures LastStateChangeAt is refreshed whenever State changes.
func (d *Deployment) BeforeUpdate(tx *gorm.DB) error {
	if tx.Statement.Changed("State") {
		tx.Statement.SetColumn("LastStateChangeAt", time.Now().UTC())
	}
	return nil
}

// The composite partial unique index on (cluster_id, namespace, app_id)
// WHERE deleted_at IS NULL is added via raw SQL in migrate.go — the same
// app cannot be deployed twice into the same (cluster, namespace).
