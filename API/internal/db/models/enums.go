// Package models holds the GORM entities for the sAId-Kick Appstore platform.
//
// Naming follows the design document: PascalCase Go structs, snake_case DB
// columns (default GORM naming strategy). All UUIDs are version 7
// (time-ordered) so primary-key indexes stay write-friendly.
package models

// Role names the seven application-level roles. Stored as varchar to keep
// migrations cheap (Postgres ENUM ALTERs are painful).
type Role string

const (
	RoleUnknown       Role = "" // zero value — uninitialized
	RoleDeveloper     Role = "developer"
	RoleAuditor       Role = "auditor"
	RoleDeployer      Role = "deployer"
	RoleClusterAdmin  Role = "cluster_admin"
	RoleOrgAdmin      Role = "org_admin"
	RolePlatformAdmin Role = "platform_admin"
	// RoleAppUser may list and open the apps their own organisation runs,
	// and nothing else. Granted automatically at first OIDC sign-in.
	RoleAppUser Role = "app_user"
)

// AllRoles is the canonical enumeration used for CHECK constraint generation
// and admin UIs.
var AllRoles = []Role{
	RoleDeveloper, RoleAuditor, RoleDeployer,
	RoleClusterAdmin, RoleOrgAdmin, RolePlatformAdmin,
	RoleAppUser,
}

// AppState is the lifecycle state of an App entity.
type AppState string

const (
	AppUnknown      AppState = "" // zero value — uninitialized
	AppDraft        AppState = "draft"
	AppSubmitted    AppState = "submitted"
	AppNeedsChanges AppState = "needs_changes"
	AppPublished    AppState = "published"
	AppRejected     AppState = "rejected"
	AppArchived     AppState = "archived"
)

var AllAppStates = []AppState{
	AppDraft, AppSubmitted, AppNeedsChanges,
	AppPublished, AppRejected, AppArchived,
}

// AppVersionState mirrors AppState but applies to a specific commit SHA.
type AppVersionState string

const (
	AppVersionUnknown      AppVersionState = "" // zero value — uninitialized
	AppVersionDraft        AppVersionState = "draft"
	AppVersionSubmitted    AppVersionState = "submitted"
	AppVersionNeedsChanges AppVersionState = "needs_changes"
	AppVersionPublished    AppVersionState = "published"
	AppVersionRejected     AppVersionState = "rejected"
	AppVersionArchived     AppVersionState = "archived"
)

var AllAppVersionStates = []AppVersionState{
	AppVersionDraft, AppVersionSubmitted, AppVersionNeedsChanges,
	AppVersionPublished, AppVersionRejected, AppVersionArchived,
}

// RepoLinkState tracks whether the GitHub App still has access to the linked
// repository.
type RepoLinkState string

const (
	RepoUnknown      RepoLinkState = "" // zero value — uninitialized
	RepoConnected    RepoLinkState = "connected"
	RepoDisconnected RepoLinkState = "disconnected"
)

// AutoValidationState is the outcome of Stage-1 (machine) audit validation.
type AutoValidationState string

const (
	AutoValUnknown AutoValidationState = "" // zero value — uninitialized
	AutoValPending AutoValidationState = "pending"
	AutoValPassed  AutoValidationState = "passed"
	AutoValFailed  AutoValidationState = "failed"
)

// Decision is the outcome of a Stage-2 (human) audit review.
type Decision string

const (
	DecisionUnknown        Decision = "" // zero value — uninitialized
	DecisionApprove        Decision = "approve"
	DecisionRequestChanges Decision = "request_changes"
	DecisionReject         Decision = "reject"
)

// ClusterScope is the access tier of a registered cluster.
type ClusterScope string

const (
	ClusterScopeUnknown    ClusterScope = ""           // zero value — uninitialized
	ClusterScopePublic     ClusterScope = "public"     // visible and deployable by every org
	ClusterScopeOrg        ClusterScope = "org"        // visible and deployable by owning org only
	ClusterScopeRestricted ClusterScope = "restricted" // visible and deployable by owning org plus orgs with an explicit ClusterAccessGrant
)

var AllClusterScopes = []ClusterScope{
	ClusterScopePublic, ClusterScopeOrg, ClusterScopeRestricted,
}

// ClusterState tracks target-cluster lifecycle.
type ClusterState string

const (
	ClusterUnknown             ClusterState = "" // zero value — uninitialized
	ClusterPendingRegistration ClusterState = "pending_registration"
	ClusterReady               ClusterState = "ready"
	ClusterReadyNoESO          ClusterState = "ready_no_eso"
	ClusterUnhealthy           ClusterState = "unhealthy"
	ClusterDecommissioned      ClusterState = "decommissioned"
)

var AllClusterStates = []ClusterState{
	ClusterPendingRegistration, ClusterReady, ClusterReadyNoESO,
	ClusterUnhealthy, ClusterDecommissioned,
}

// DeploymentState is exhaustive for the deployment state machine. Failure
// states starting with failed_ are retryable; ready and deleted are terminal.
type DeploymentState string

const (
	DeploymentUnknown             DeploymentState = "" // zero value — uninitialized
	DeploymentPending             DeploymentState = "pending"
	DeploymentWaitingForCluster   DeploymentState = "waiting_for_cluster"
	DeploymentApplying            DeploymentState = "applying"
	DeploymentSecretSync          DeploymentState = "secret_sync"
	DeploymentReconciling         DeploymentState = "reconciling"
	DeploymentReady               DeploymentState = "ready"
	DeploymentDegraded            DeploymentState = "degraded"
	DeploymentFailedRender        DeploymentState = "failed_render"
	DeploymentFailedVault         DeploymentState = "failed_vault"
	DeploymentFailedOrchestrator  DeploymentState = "failed_orchestrator"
	DeploymentFailedSecretSync    DeploymentState = "failed_secret_sync"
	DeploymentPendingOrchestrator DeploymentState = "pending_orchestrator"
	DeploymentDeleting            DeploymentState = "deleting"
	DeploymentDeleted             DeploymentState = "deleted"
)

var AllDeploymentStates = []DeploymentState{
	DeploymentPending, DeploymentWaitingForCluster, DeploymentApplying,
	DeploymentSecretSync, DeploymentReconciling, DeploymentReady,
	DeploymentDegraded, DeploymentFailedRender, DeploymentFailedVault,
	DeploymentFailedOrchestrator, DeploymentFailedSecretSync,
	DeploymentPendingOrchestrator, DeploymentDeleting, DeploymentDeleted,
}
