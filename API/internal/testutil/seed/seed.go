// Package seed inserts the rows integration tests build on. Each builder
// takes the model to insert, fills any required field the caller left empty
// with a unique default, creates the row and returns it with its ID set.
package seed

import (
	"fmt"
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// SeedOrg inserts org. Empty Slug, DisplayName and ContactEmail get defaults.
func SeedOrg(t testing.TB, h *db.Handle, org models.Organization) models.Organization {
	t.Helper()
	suffix := shortID()
	if org.Slug == "" {
		org.Slug = "org-" + suffix
	}
	if org.DisplayName == "" {
		org.DisplayName = "Org " + suffix
	}
	if org.ContactEmail == "" {
		org.ContactEmail = "contact-" + suffix + "@example.com"
	}
	create(t, h, &org)
	return org
}

// SeedUser inserts u. OrganizationID is required; empty ExternalSubject,
// Email and DisplayName get defaults.
func SeedUser(t testing.TB, h *db.Handle, u models.User) models.User {
	t.Helper()
	if u.OrganizationID == uuid.Nil {
		t.Fatal("seed.SeedUser: OrganizationID is required")
	}
	suffix := shortID()
	if u.ExternalSubject == "" {
		u.ExternalSubject = "sub-" + suffix
	}
	if u.Email == "" {
		u.Email = "user-" + suffix + "@example.com"
	}
	if u.DisplayName == "" {
		u.DisplayName = "User " + suffix
	}
	create(t, h, &u)
	return u
}

// SeedRoleAssignment inserts ra. UserID and Role are required; an empty
// GrantedByUserID defaults to UserID.
func SeedRoleAssignment(t testing.TB, h *db.Handle, ra models.RoleAssignment) models.RoleAssignment {
	t.Helper()
	if ra.UserID == uuid.Nil || ra.Role == models.RoleUnknown {
		t.Fatal("seed.SeedRoleAssignment: UserID and Role are required")
	}
	if ra.GrantedByUserID == uuid.Nil {
		ra.GrantedByUserID = ra.UserID
	}
	create(t, h, &ra)
	return ra
}

// SeedCluster inserts c. An empty Name gets a default; empty Scope and State
// default to a public, ready cluster.
func SeedCluster(t testing.TB, h *db.Handle, c models.Cluster) models.Cluster {
	t.Helper()
	if c.Name == "" {
		c.Name = "cluster-" + shortID()
	}
	if c.Scope == "" {
		c.Scope = models.ClusterScopePublic
	}
	if c.State == "" {
		c.State = models.ClusterReady
	}
	create(t, h, &c)
	return c
}

// SeedApp inserts a. OwningOrganizationID and OwningUserID are required.
// Empty fields default to a published app on installation 1, repo
// owner/repo-<id>, branch main.
func SeedApp(t testing.TB, h *db.Handle, a models.App) models.App {
	t.Helper()
	if a.OwningOrganizationID == uuid.Nil || a.OwningUserID == uuid.Nil {
		t.Fatal("seed.SeedApp: OwningOrganizationID and OwningUserID are required")
	}
	uid := uuid.Must(uuid.NewV7()).String()
	if a.Slug == "" {
		a.Slug = "app-" + uid
	}
	if a.GitHubInstallationID == 0 {
		a.GitHubInstallationID = 1
	}
	if a.GitHubRepoOwner == "" {
		a.GitHubRepoOwner = "owner"
	}
	if a.GitHubRepoName == "" {
		a.GitHubRepoName = "repo-" + uid
	}
	if a.GitHubDefaultBranch == "" {
		a.GitHubDefaultBranch = "main"
	}
	if a.State == "" {
		a.State = models.AppPublished
	}
	create(t, h, &a)
	return a
}

// SeedAppVersion inserts v together with the AuditSubmission it points at.
// AppID and State are required.
//
// AppVersion.AuditSubmissionID and AuditSubmission.AppVersionID reference
// each other. Neither has a DB-level FK constraint (see
// models/app_version.go), so both IDs are minted up front and the
// submission is inserted first.
//
// Empty fields default to ref main, a random SHA, a minimal manifest, an
// empty values schema and chart test@1.0.0.
func SeedAppVersion(t testing.TB, h *db.Handle, v models.AppVersion) (models.AppVersion, models.AuditSubmission) {
	t.Helper()
	if v.AppID == uuid.Nil {
		t.Fatal("seed.SeedAppVersion: AppID is required")
	}
	if v.State == "" {
		t.Fatal("seed.SeedAppVersion: State is required")
	}
	if v.ID == uuid.Nil {
		v.ID = uuid.Must(uuid.NewV7())
	}
	if v.AuditSubmissionID == uuid.Nil {
		v.AuditSubmissionID = uuid.Must(uuid.NewV7())
	}
	if v.GitRef == "" {
		v.GitRef = "main"
	}
	if v.GitSHA == "" {
		v.GitSHA = fmt.Sprintf("%x%x", uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))[:40]
	}
	if v.AppstoreYAML == nil {
		v.AppstoreYAML = datatypes.JSON(`{"apiVersion":"appstore.gov.nl/v1","kind":"Application","metadata":{"name":"test"},"spec":{"ai":{"risk_class":"minimal"}}}`)
	}
	if v.ValuesSchema == nil {
		v.ValuesSchema = datatypes.JSON(`{}`)
	}
	if v.ChartMetadata == nil {
		v.ChartMetadata = datatypes.JSON(`{"name":"test","version":"1.0.0"}`)
	}

	sub := models.AuditSubmission{
		IDModel:             models.IDModel{ID: v.AuditSubmissionID},
		AppVersionID:        v.ID,
		FormResponses:       datatypes.JSON(`{}`),
		FormVersion:         "v1",
		EvidenceFiles:       datatypes.JSON(`[]`),
		AutoValidationState: models.AutoValPassed,
	}
	create(t, h, &sub)
	create(t, h, &v)
	return v, sub
}

// SeedPublishedVersion is SeedAppVersion with State set to published.
func SeedPublishedVersion(t testing.TB, h *db.Handle, v models.AppVersion) (models.AppVersion, models.AuditSubmission) {
	t.Helper()
	v.State = models.AppVersionPublished
	return SeedAppVersion(t, h, v)
}

// SeedAuditDecision inserts d. AuditSubmissionID, Decision and
// DecidedByUserID are required; an empty Rationale gets a default that
// satisfies the length check.
func SeedAuditDecision(t testing.TB, h *db.Handle, d models.AuditDecision) models.AuditDecision {
	t.Helper()
	if d.AuditSubmissionID == uuid.Nil || d.Decision == "" || d.DecidedByUserID == uuid.Nil {
		t.Fatal("seed.SeedAuditDecision: AuditSubmissionID, Decision and DecidedByUserID are required")
	}
	if d.Rationale == "" {
		d.Rationale = "Seeded audit decision for an integration test."
	}
	create(t, h, &d)
	return d
}

// SeedDeployment inserts d. AppID, AppVersionID, ClusterID,
// OwningOrganizationID and DeployerUserID are required. Empty fields default
// to namespace "default", no values, no secret refs and state pending.
func SeedDeployment(t testing.TB, h *db.Handle, d models.Deployment) models.Deployment {
	t.Helper()
	for name, id := range map[string]uuid.UUID{
		"AppID":                d.AppID,
		"AppVersionID":         d.AppVersionID,
		"ClusterID":            d.ClusterID,
		"OwningOrganizationID": d.OwningOrganizationID,
		"DeployerUserID":       d.DeployerUserID,
	} {
		if id == uuid.Nil {
			t.Fatalf("seed.SeedDeployment: %s is required", name)
		}
	}
	if d.Namespace == "" {
		d.Namespace = "default"
	}
	if d.Values == nil {
		d.Values = datatypes.JSON(`{}`)
	}
	if d.SecretRefs == nil {
		d.SecretRefs = datatypes.JSON(`[]`)
	}
	if d.State == "" {
		d.State = models.DeploymentPending
	}
	create(t, h, &d)
	return d
}

// SeedRevokedSession inserts r. A nil JTI or UserID gets a fresh UUID; a
// zero ExpiresAt defaults to one hour from now.
func SeedRevokedSession(t testing.TB, h *db.Handle, r models.RevokedSession) models.RevokedSession {
	t.Helper()
	if r.JTI == uuid.Nil {
		r.JTI = uuid.New()
	}
	if r.UserID == uuid.Nil {
		r.UserID = uuid.New()
	}
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = time.Now().Add(time.Hour)
	}
	create(t, h, &r)
	return r
}

func create(t testing.TB, h *db.Handle, row any) {
	t.Helper()
	if err := h.RW.WithContext(t.Context()).Create(row).Error; err != nil {
		t.Fatalf("seed: create %T: %v", row, err)
	}
}

// shortID returns 8 random hex characters for unique default names.
func shortID() string {
	return uuid.NewString()[:8]
}
