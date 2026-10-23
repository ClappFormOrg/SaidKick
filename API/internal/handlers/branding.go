package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/branding"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Instance-wide branding (#158): one platform_settings row holding six
// colours and an optional PNG logo. Reads are anonymous because every page,
// signed in or not, renders with the brand. Writes are platform_admin only.
//
// Nothing is cached in process. The hub runs several API replicas, and a
// cache on one would serve the old palette after a write on another; the
// read is one single-row query that skips the logo bytes. Clients revalidate
// with the ETag, which is derived from updated_at.

// brandingLogoPath is the API path of the logo. The webapp serves it to
// browsers through its own /branding/logo route, so the CSP's img-src 'self'
// covers it whatever origin the API is on.
const brandingLogoPath = "/api/v1/branding/logo"

// maxLogoRequestBytes caps the whole multipart body; the file itself is
// capped at branding.MaxLogoBytes after it is read.
const maxLogoRequestBytes = 1 << 20

// brandingColumns is every column except logo_content, so a palette read
// never loads the logo bytes.
var brandingColumns = []string{
	"id", "cta", "primary_color", "secondary", "surface", "body_text", "border",
	"logo_content_type", "logo_width", "logo_height", "logo_alt",
	"updated_by_user_id", "created_at", "updated_at",
}

// BrandingColors is the six operator-set colours, as lowercase #rrggbb. It is
// also the body of PUT /api/v1/branding.
type BrandingColors struct {
	CTA       string `json:"cta"`
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Surface   string `json:"surface"`
	BodyText  string `json:"body_text"`
	Border    string `json:"border"`
}

// BrandingLogo describes the stored logo. The bytes are served separately.
type BrandingLogo struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Alt    string `json:"alt"`
}

// BrandingResponse is the body of GET /api/v1/branding and of every write.
type BrandingResponse struct {
	Colors    BrandingColors  `json:"colors"`
	Tokens    branding.Tokens `json:"tokens"`
	IsDefault bool            `json:"is_default"`
	Logo      *BrandingLogo   `json:"logo"`
	// ContrastFailures is empty for every palette the API accepts; it is
	// reported so a stricter check in a later release shows up here first.
	ContrastFailures []branding.ContrastFailure `json:"contrast_failures"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

func colorsOf(s *models.PlatformSettings) BrandingColors {
	return BrandingColors{
		CTA: s.CTA, Primary: s.PrimaryColor, Secondary: s.Secondary,
		Surface: s.Surface, BodyText: s.BodyText, Border: s.Border,
	}
}

// parsePalette validates the six colours and returns a field error for each
// one that is not #RRGGBB.
func parsePalette(c BrandingColors) (branding.Palette, []apierror.FieldError) {
	var p branding.Palette
	var fields []apierror.FieldError
	for _, f := range []struct {
		path  string
		value string
		dst   *branding.RGB
	}{
		{"cta", c.CTA, &p.CTA},
		{"primary", c.Primary, &p.Primary},
		{"secondary", c.Secondary, &p.Secondary},
		{"surface", c.Surface, &p.Surface},
		{"body_text", c.BodyText, &p.BodyText},
		{"border", c.Border, &p.Border},
	} {
		rgb, err := branding.ParseHex(f.value)
		if err != nil {
			fields = append(fields, apierror.FieldError{
				Path: f.path, Code: "invalid_color", Message: f.path + " must be a #RRGGBB colour",
			})
			continue
		}
		*f.dst = rgb
	}
	return p, fields
}

func brandingResponse(s *models.PlatformSettings) (BrandingResponse, error) {
	p, fields := parsePalette(colorsOf(s))
	if len(fields) > 0 {
		// The table's CHECK constraint makes this unreachable.
		return BrandingResponse{}, fmt.Errorf("platform_settings holds an invalid colour: %s", fields[0].Path)
	}
	out := BrandingResponse{
		Colors:           colorsOf(s),
		Tokens:           branding.Derive(p),
		IsDefault:        branding.IsDefault(p),
		ContrastFailures: branding.Contrast(p),
		UpdatedAt:        s.UpdatedAt,
	}
	if s.HasLogo() && s.LogoWidth != nil && s.LogoHeight != nil && s.LogoAlt != nil {
		out.Logo = &BrandingLogo{URL: brandingLogoPath, Width: *s.LogoWidth, Height: *s.LogoHeight, Alt: *s.LogoAlt}
	}
	return out, nil
}

// brandingETag is a strong validator that changes on every write.
func brandingETag(updatedAt time.Time) string {
	return fmt.Sprintf(`"%x"`, updatedAt.UnixNano())
}

// notModified answers 304 when the request's If-None-Match names etag.
func notModified(c echo.Context, etag string) bool {
	for v := range strings.SplitSeq(c.Request().Header.Get("If-None-Match"), ",") {
		v = strings.TrimSpace(v)
		if v == etag || v == "W/"+etag || v == "*" {
			return true
		}
	}
	return false
}

func (h *Handlers) loadPlatformSettings(ctx context.Context, tx *gorm.DB) (models.PlatformSettings, error) {
	var s models.PlatformSettings
	err := tx.WithContext(ctx).Select(brandingColumns).Take(&s).Error
	return s, err
}

// GetBranding returns the palette, its derived tokens and the logo metadata.
// Anonymous.
func (h *Handlers) GetBranding(c echo.Context) error {
	s, err := h.loadPlatformSettings(c.Request().Context(), h.DB.RW)
	if err != nil {
		return err
	}
	out, err := brandingResponse(&s)
	if err != nil {
		return err
	}
	etag := brandingETag(s.UpdatedAt)
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "no-cache")
	if notModified(c, etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.JSON(http.StatusOK, out)
}

// GetBrandingLogo serves the logo bytes. Anonymous. The Content-Type is the
// constant image/png whatever the upload declared; the upload was decoded
// as a PNG before it was stored.
func (h *Handlers) GetBrandingLogo(c echo.Context) error {
	var s models.PlatformSettings
	if err := h.DB.RW.WithContext(c.Request().Context()).
		Select("logo_content", "logo_content_type", "updated_at").
		Take(&s).Error; err != nil {
		return err
	}
	if !s.HasLogo() || len(s.LogoContent) == 0 {
		return apierror.New(http.StatusNotFound, "not_found", "no logo is set")
	}
	etag := brandingETag(s.UpdatedAt)
	hdr := c.Response().Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Content-Type-Options", "nosniff")
	if notModified(c, etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, branding.LogoContentType, s.LogoContent)
}

// requirePlatformAdmin mirrors the route's role gate so a routing mistake
// cannot expose a write.
func requirePlatformAdmin(c echo.Context) (*auth.Principal, error) {
	p := auth.PrincipalFromContext(c.Request().Context())
	if p == nil {
		return nil, apierror.New(http.StatusUnauthorized, "unauthorized", "authentication required")
	}
	if !p.Has(models.RolePlatformAdmin) {
		return nil, apierror.New(http.StatusForbidden, "forbidden", "missing required role")
	}
	return p, nil
}

// brandingWrite is one change to the settings row: the columns to set, and
// the audit payload, both computed from the locked row as it was.
type brandingWrite func(tx *gorm.DB, before *models.PlatformSettings) (updates map[string]any, payload map[string]any, err error)

// writeBranding locks the row, applies write, emits one audit event of
// eventType and responds with the new state.
func (h *Handlers) writeBranding(c echo.Context, p *auth.Principal, eventType string, write brandingWrite) error {
	ctx := c.Request().Context()
	var after models.PlatformSettings
	var payload map[string]any
	err := h.DB.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before models.PlatformSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(brandingColumns).Take(&before).Error; err != nil {
			return err
		}
		updates, pl, err := write(tx, &before)
		if err != nil {
			return err
		}
		payload = pl
		// A principal without a users row (an API key's owner is always
		// one; dev mode may not be) leaves the column NULL instead of
		// failing the foreign key.
		updates["updated_by_user_id"] = gorm.Expr("(SELECT id FROM users WHERE id = ?)", p.UserID)
		updates["updated_at"] = time.Now().UTC()
		if err := tx.Model(&models.PlatformSettings{}).Where("id = ?", before.ID).Updates(updates).Error; err != nil {
			return err
		}
		after, err = h.loadPlatformSettings(ctx, tx)
		return err
	})
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return err
	}

	_ = h.Audit.Log(ctx, audit.Event{
		Type:                eventType,
		ActorUserID:         &p.UserID,
		ActorOrganizationID: &p.OrganizationID,
		SubjectType:         "platform_settings",
		SubjectID:           after.ID,
		Payload:             payload,
	})

	out, err := brandingResponse(&after)
	if err != nil {
		return err
	}
	c.Response().Header().Set("ETag", brandingETag(after.UpdatedAt))
	return c.JSON(http.StatusOK, out)
}

func colorUpdates(c BrandingColors) map[string]any {
	return map[string]any{
		"cta": c.CTA, "primary_color": c.Primary, "secondary": c.Secondary,
		"surface": c.Surface, "body_text": c.BodyText, "border": c.Border,
	}
}

func paletteColors(p branding.Palette) BrandingColors {
	return BrandingColors{
		CTA: p.CTA.Hex(), Primary: p.Primary.Hex(), Secondary: p.Secondary.Hex(),
		Surface: p.Surface.Hex(), BodyText: p.BodyText.Hex(), Border: p.Border.Hex(),
	}
}

// contrastError is the 422 for a palette that fails WCAG AA: one field error
// per failing pair, plus the measured ratios under details.
func contrastError(failures []branding.ContrastFailure) *apierror.Error {
	fields := make([]apierror.FieldError, 0, len(failures))
	for _, f := range failures {
		fields = append(fields, apierror.FieldError{
			Path: "contrast." + f.Pair,
			Code: "contrast_too_low",
			Message: fmt.Sprintf("%s on %s is %.2f:1; WCAG AA needs %.1f:1",
				f.Foreground, f.Background, f.Ratio, f.Required),
		})
	}
	e := apierror.NewValidation("the palette fails WCAG AA contrast", fields)
	e.Details = map[string]any{"contrast_failures": failures}
	return e
}

// PutBranding replaces the six colours. platform_admin only. A palette with
// any checked text pair below WCAG AA is refused with 422 and the failing
// pairs; nothing is stored.
func (h *Handlers) PutBranding(c echo.Context) error {
	p, err := requirePlatformAdmin(c)
	if err != nil {
		return err
	}
	var req BrandingColors
	if err := c.Bind(&req); err != nil {
		return apierror.New(http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	palette, fields := parsePalette(req)
	if len(fields) > 0 {
		return apierror.NewValidation("validation failed", fields)
	}
	if failures := branding.Contrast(palette); len(failures) > 0 {
		return contrastError(failures)
	}
	next := paletteColors(palette)
	return h.writeBranding(c, p, "branding.updated", func(_ *gorm.DB, before *models.PlatformSettings) (map[string]any, map[string]any, error) {
		return colorUpdates(next), map[string]any{"before": colorsOf(before), "after": next}, nil
	})
}

// DeleteBranding resets the six colours to the default palette. The logo is
// left alone; DELETE /branding/logo removes it. platform_admin only.
func (h *Handlers) DeleteBranding(c echo.Context) error {
	p, err := requirePlatformAdmin(c)
	if err != nil {
		return err
	}
	next := paletteColors(branding.Default())
	return h.writeBranding(c, p, "branding.reset", func(_ *gorm.DB, before *models.PlatformSettings) (map[string]any, map[string]any, error) {
		return colorUpdates(next), map[string]any{"before": colorsOf(before), "after": next}, nil
	})
}

// logoAudit is the audit record of a logo: its metadata and a digest of its
// bytes, never the bytes themselves.
func logoAudit(width, height int, alt string, content []byte) map[string]any {
	sum := sha256.Sum256(content)
	return map[string]any{
		"width": width, "height": height, "alt": alt,
		"size_bytes": len(content), "sha256": hex.EncodeToString(sum[:]),
	}
}

// storedLogoAudit reads the logo bytes of the locked row for the audit
// record, or returns nil when no logo is set.
func storedLogoAudit(tx *gorm.DB, before *models.PlatformSettings) (map[string]any, error) {
	if !before.HasLogo() || before.LogoWidth == nil || before.LogoHeight == nil || before.LogoAlt == nil {
		return nil, nil
	}
	var content []byte
	if err := tx.Model(&models.PlatformSettings{}).Select("logo_content").
		Where("id = ?", before.ID).Row().Scan(&content); err != nil {
		return nil, err
	}
	return logoAudit(*before.LogoWidth, *before.LogoHeight, *before.LogoAlt, content), nil
}

func logoFieldError(code, message string) *apierror.Error {
	return apierror.NewValidation("validation failed", []apierror.FieldError{
		{Path: "file", Code: code, Message: message},
	})
}

// readLogoUpload reads the multipart "file" part, at most one byte past the
// file limit so an oversize file is detected without reading all of it.
func readLogoUpload(c echo.Context) ([]byte, error) {
	req := c.Request()
	req.Body = http.MaxBytesReader(c.Response(), req.Body, maxLogoRequestBytes)
	fh, err := c.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "request body too large") {
			return nil, apierror.New(http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("the upload exceeds %d bytes", maxLogoRequestBytes))
		}
		return nil, logoFieldError("required", "a multipart file field named \"file\" is required")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, branding.MaxLogoBytes+1))
}

// PostBrandingLogo stores a new logo: a multipart form with a PNG "file" and
// its "alt" text. platform_admin only. The file's declared type and name are
// ignored; see branding.ValidateLogo for the checks. A rejected upload
// stores nothing.
func (h *Handlers) PostBrandingLogo(c echo.Context) error {
	p, err := requirePlatformAdmin(c)
	if err != nil {
		return err
	}
	raw, err := readLogoUpload(c)
	if err != nil {
		return err
	}
	width, height, err := branding.ValidateLogo(raw)
	if err != nil {
		var le *branding.LogoError
		if errors.As(err, &le) {
			return logoFieldError(le.Code, le.Error())
		}
		return err
	}
	alt, err := branding.NormaliseLogoAlt(c.FormValue("alt"))
	if err != nil {
		return apierror.NewValidation("validation failed", []apierror.FieldError{
			{Path: "alt", Code: "invalid", Message: "alt must be 1 to 255 characters"},
		})
	}

	return h.writeBranding(c, p, "branding.logo_updated", func(tx *gorm.DB, before *models.PlatformSettings) (map[string]any, map[string]any, error) {
		prev, err := storedLogoAudit(tx, before)
		if err != nil {
			return nil, nil, err
		}
		updates := map[string]any{
			"logo_content":      raw,
			"logo_content_type": branding.LogoContentType,
			"logo_width":        width,
			"logo_height":       height,
			"logo_alt":          alt,
		}
		return updates, map[string]any{"before": prev, "after": logoAudit(width, height, alt, raw)}, nil
	})
}

// DeleteBrandingLogo removes the logo, so the header shows the default
// lockup again. platform_admin only. 404 when no logo is set.
func (h *Handlers) DeleteBrandingLogo(c echo.Context) error {
	p, err := requirePlatformAdmin(c)
	if err != nil {
		return err
	}
	return h.writeBranding(c, p, "branding.logo_deleted", func(tx *gorm.DB, before *models.PlatformSettings) (map[string]any, map[string]any, error) {
		prev, err := storedLogoAudit(tx, before)
		if err != nil {
			return nil, nil, err
		}
		if prev == nil {
			return nil, nil, apierror.New(http.StatusNotFound, "not_found", "no logo is set")
		}
		updates := map[string]any{
			"logo_content":      nil,
			"logo_content_type": nil,
			"logo_width":        nil,
			"logo_height":       nil,
			"logo_alt":          nil,
		}
		return updates, map[string]any{"before": prev, "after": nil}, nil
	})
}
