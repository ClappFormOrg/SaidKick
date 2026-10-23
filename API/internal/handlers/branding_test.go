package handlers_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/branding"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	"github.com/ClappFormOrg/SaidKick/API/internal/server"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveBranding sends req through the real route table with the server's
// error renderer and metrics middleware, so status codes and bodies are the
// ones a client gets.
func serveBranding(h *handlers.Handlers, req *http.Request) *httptest.ResponseRecorder {
	e := echo.New()
	e.HTTPErrorHandler = server.ErrorHandler
	e.Use(server.MetricsMiddleware())
	h.Register(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

type brandingFixture struct {
	handle *db.Handle
	h      *handlers.Handlers
	audit  *recordingAuditLogger
	admin  models.User
}

func newBrandingFixture(t *testing.T) brandingFixture {
	t.Helper()
	handle := testutil.NewPostgresDB(t)
	rec := &recordingAuditLogger{}
	h := newTestHandlers(t, handle)
	h.Audit = rec
	org := seed.SeedOrg(t, handle, models.Organization{})
	return brandingFixture{
		handle: handle, h: h, audit: rec,
		admin: seed.SeedUser(t, handle, models.User{OrganizationID: org.ID}),
	}
}

func (f brandingFixture) asAdmin(req *http.Request) *http.Request {
	return asPrincipal(req, principal(f.admin, models.RolePlatformAdmin))
}

func (f brandingFixture) settings(t *testing.T) models.PlatformSettings {
	t.Helper()
	var s models.PlatformSettings
	require.NoError(t, f.handle.RW.WithContext(t.Context()).Take(&s).Error)
	return s
}

func decodeBranding(t *testing.T, rec *httptest.ResponseRecorder) handlers.BrandingResponse {
	t.Helper()
	var out handlers.BrandingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// errorBody is the server's error envelope.
type errorBody struct {
	Error struct {
		Code   string `json:"code"`
		Errors []struct {
			Path string `json:"path"`
			Code string `json:"code"`
		} `json:"errors"`
		Details struct {
			ContrastFailures []branding.ContrastFailure `json:"contrast_failures"`
		} `json:"details"`
	} `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var out errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

const customPalette = `{"cta":"#0B5CAD","primary":"#1a1a6e","secondary":"#eef3ff",` +
	`"surface":"#f7f7f7","body_text":"#111111","border":"#d0d0d0"}`

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// logoRequest builds a multipart logo upload. contentType is what the client
// claims the file is; the API must ignore it.
func logoRequest(t *testing.T, filename, contentType string, content []byte, alt *string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if content != nil {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		hdr.Set("Content-Type", contentType)
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}
	if alt != nil {
		require.NoError(t, mw.WriteField("alt", *alt))
	}
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/branding/logo", &body)
	req.Header.Set(echo.HeaderContentType, mw.FormDataContentType())
	return req
}

func strPtr(s string) *string { return &s }

func TestGetBrandingIsAnonymousAndDefault(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)

	rec := serveBranding(f.h, newRequest(http.MethodGet, "/api/v1/branding", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeBranding(t, rec)
	assert.True(t, out.IsDefault)
	assert.Equal(t, branding.DefaultTokens(), out.Tokens)
	assert.Equal(t, "#ca005d", out.Colors.CTA)
	assert.Nil(t, out.Logo)
	assert.Empty(t, out.ContrastFailures)
	assert.NotContains(t, rec.Body.String(), "logo_content")
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))

	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	req := newRequest(http.MethodGet, "/api/v1/branding", "")
	req.Header.Set("If-None-Match", `"other", `+etag)
	rec = serveBranding(f.h, req)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Zero(t, rec.Body.Len())

	rec = serveBranding(f.h, newRequest(http.MethodGet, "/api/v1/branding/logo", ""))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBrandingWritesArePlatformAdminOnly(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	before := f.settings(t)

	writes := []func() *http.Request{
		func() *http.Request { return newRequest(http.MethodPut, "/api/v1/branding", customPalette) },
		func() *http.Request { return newRequest(http.MethodDelete, "/api/v1/branding", "") },
		func() *http.Request {
			return logoRequest(t, "logo.png", "image/png", pngBytes(t, 32, 32), strPtr("Logo"))
		},
		func() *http.Request { return newRequest(http.MethodDelete, "/api/v1/branding/logo", "") },
	}
	for _, build := range writes {
		req := build()
		name := req.Method + " " + req.URL.Path
		t.Run(name+"/anonymous", func(t *testing.T) {
			assert.Equal(t, http.StatusUnauthorized, serveBranding(f.h, build()).Code)
		})
		for _, role := range models.AllRoles {
			if role == models.RolePlatformAdmin {
				continue
			}
			t.Run(name+"/"+string(role), func(t *testing.T) {
				req := asPrincipal(build(), principal(f.admin, role))
				assert.Equal(t, http.StatusForbidden, serveBranding(f.h, req).Code)
			})
		}
	}
	// Every role but platform_admin at once is still refused.
	others := make([]models.Role, 0, len(models.AllRoles))
	for _, r := range models.AllRoles {
		if r != models.RolePlatformAdmin {
			others = append(others, r)
		}
	}
	req := asPrincipal(newRequest(http.MethodPut, "/api/v1/branding", customPalette), principal(f.admin, others...))
	assert.Equal(t, http.StatusForbidden, serveBranding(f.h, req).Code)

	// The handlers repeat the gate in case a route loses its middleware.
	for _, call := range []func(echo.Context) error{f.h.PutBranding, f.h.DeleteBranding, f.h.PostBrandingLogo, f.h.DeleteBrandingLogo} {
		c, _ := echoContext(echo.New(), asPrincipal(newRequest(http.MethodPut, "/", customPalette), principal(f.admin, models.RoleOrgAdmin)))
		requireAPIError(t, call(c), http.StatusForbidden)
		c, _ = echoContext(echo.New(), newRequest(http.MethodPut, "/", customPalette))
		requireAPIError(t, call(c), http.StatusUnauthorized)
	}

	assert.Equal(t, before.UpdatedAt, f.settings(t).UpdatedAt, "a refused caller changed the row")
	assert.Empty(t, f.audit.events)
}

func TestPutBranding(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	before := f.settings(t)

	rec := serveBranding(f.h, f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding", customPalette)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeBranding(t, rec)
	assert.False(t, out.IsDefault)
	assert.Equal(t, "#0b5cad", out.Colors.CTA, "stored lowercase")
	assert.Equal(t, "#0b5cad", out.Tokens.CTA)
	assert.Equal(t, "#094a8a", out.Tokens.CTAHover)
	assert.NotEqual(t, before.UpdatedAt, out.UpdatedAt)
	assert.Equal(t, rec.Header().Get("ETag"), serveBranding(f.h, newRequest(http.MethodGet, "/api/v1/branding", "")).Header().Get("ETag"))

	s := f.settings(t)
	assert.Equal(t, "#1a1a6e", s.PrimaryColor)
	require.NotNil(t, s.UpdatedByUserID)
	assert.Equal(t, f.admin.ID, *s.UpdatedByUserID)

	require.Len(t, f.audit.events, 1)
	ev := f.audit.events[0]
	assert.Equal(t, "branding.updated", ev.Type)
	assert.Equal(t, "platform_settings", ev.SubjectType)
	assert.Equal(t, s.ID, ev.SubjectID)
	assert.Equal(t, f.admin.ID, *ev.ActorUserID)
	payload := ev.Payload.(map[string]any)
	assert.Equal(t, "#ca005d", payload["before"].(handlers.BrandingColors).CTA)
	assert.Equal(t, "#0b5cad", payload["after"].(handlers.BrandingColors).CTA)
}

// The hub runs several API replicas and nothing is cached in process, so a
// write through one is read back by another straight away.
func TestBrandingWriteVisibleFromEveryReplica(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	replicaA := f.h
	replicaB := newTestHandlers(t, f.handle)

	first := serveBranding(replicaB, newRequest(http.MethodGet, "/api/v1/branding", ""))
	require.Equal(t, http.StatusOK, first.Code)
	require.True(t, decodeBranding(t, first).IsDefault)

	require.Equal(t, http.StatusOK, serveBranding(replicaA,
		f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding", customPalette))).Code)

	req := newRequest(http.MethodGet, "/api/v1/branding", "")
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))
	rec := serveBranding(replicaB, req)
	require.Equal(t, http.StatusOK, rec.Code, "the old ETag no longer matches")
	assert.Equal(t, "#0b5cad", decodeBranding(t, rec).Colors.CTA)
}

func TestPutBrandingValidation(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	before := f.settings(t)

	rec := serveBranding(f.h, f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding",
		`{"cta":"#abc","primary":"#154273","secondary":"red","surface":"#f1f5f9","body_text":"#0f172a;}","border":""}`)))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	paths := []string{}
	for _, e := range decodeError(t, rec).Error.Errors {
		assert.Equal(t, "invalid_color", e.Code)
		paths = append(paths, e.Path)
	}
	assert.Equal(t, []string{"cta", "secondary", "body_text", "border"}, paths)

	rec = serveBranding(f.h, f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding", `{"cta":`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	assert.Equal(t, before.UpdatedAt, f.settings(t).UpdatedAt)
	assert.Empty(t, f.audit.events)
}

// A palette below WCAG AA is refused with the failing pairs (#48's #009dff).
func TestPutBrandingRefusesLowContrast(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	before := f.settings(t)

	body := `{"cta":"#009dff","primary":"#154273","secondary":"#e1f0fa","surface":"#f1f5f9","body_text":"#0f172a","border":"#cbd5e1"}`
	rec := serveBranding(f.h, f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding", body)))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	e := decodeError(t, rec).Error
	assert.Equal(t, "validation_failed", e.Code)
	require.Len(t, e.Errors, 2)
	assert.Equal(t, "contrast.white_on_cta", e.Errors[0].Path)
	assert.Equal(t, "contrast_too_low", e.Errors[0].Code)
	assert.Equal(t, "contrast.white_on_cta_hover", e.Errors[1].Path)
	require.Len(t, e.Details.ContrastFailures, 2)
	assert.InDelta(t, 2.88, e.Details.ContrastFailures[0].Ratio, 0)
	assert.Contains(t, rec.Body.String(), "#ffffff on #009dff is 2.88:1")

	assert.Equal(t, before.CTA, f.settings(t).CTA, "nothing is stored")
	assert.Empty(t, f.audit.events)
}

func TestDeleteBrandingResetsColoursOnly(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	require.Equal(t, http.StatusOK, serveBranding(f.h, f.asAdmin(newRequest(http.MethodPut, "/api/v1/branding", customPalette))).Code)
	require.Equal(t, http.StatusOK, serveBranding(f.h, f.asAdmin(
		logoRequest(t, "logo.png", "image/png", pngBytes(t, 120, 40), strPtr("Gemeente Voorbeeld")))).Code)

	rec := serveBranding(f.h, f.asAdmin(newRequest(http.MethodDelete, "/api/v1/branding", "")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeBranding(t, rec)
	assert.True(t, out.IsDefault)
	require.NotNil(t, out.Logo, "reset keeps the logo")
	assert.Equal(t, "Gemeente Voorbeeld", out.Logo.Alt)

	require.Len(t, f.audit.events, 3)
	ev := f.audit.events[2]
	assert.Equal(t, "branding.reset", ev.Type)
	payload := ev.Payload.(map[string]any)
	assert.Equal(t, "#0b5cad", payload["before"].(handlers.BrandingColors).CTA)
	assert.Equal(t, "#ca005d", payload["after"].(handlers.BrandingColors).CTA)
}

func TestBrandingLogoLifecycle(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	raw := pngBytes(t, 240, 80)

	// The declared type is ignored: this valid PNG claims to be SVG.
	rec := serveBranding(f.h, f.asAdmin(logoRequest(t, "logo.svg", "image/svg+xml", raw, strPtr("  Gemeente\nVoorbeeld  "))))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeBranding(t, rec)
	require.NotNil(t, out.Logo)
	assert.Equal(t, handlers.BrandingLogo{URL: "/api/v1/branding/logo", Width: 240, Height: 80, Alt: "Gemeente Voorbeeld"}, *out.Logo)

	rec = serveBranding(f.h, newRequest(http.MethodGet, "/api/v1/branding/logo", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get(echo.HeaderContentType))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, raw, rec.Body.Bytes())
	etag := rec.Header().Get("ETag")
	req := newRequest(http.MethodGet, "/api/v1/branding/logo", "")
	req.Header.Set("If-None-Match", etag)
	assert.Equal(t, http.StatusNotModified, serveBranding(f.h, req).Code)

	require.Len(t, f.audit.events, 1)
	ev := f.audit.events[0]
	assert.Equal(t, "branding.logo_updated", ev.Type)
	payload := ev.Payload.(map[string]any)
	assert.Nil(t, payload["before"])
	after := payload["after"].(map[string]any)
	assert.Equal(t, 240, after["width"])
	assert.Equal(t, len(raw), after["size_bytes"])
	assert.Len(t, after["sha256"], 64)

	// Replacing it records the old one as before.
	replacement := pngBytes(t, 64, 64)
	require.Equal(t, http.StatusOK, serveBranding(f.h, f.asAdmin(logoRequest(t, "b.png", "image/png", replacement, strPtr("B")))).Code)
	require.Len(t, f.audit.events, 2)
	assert.Equal(t, after, f.audit.events[1].Payload.(map[string]any)["before"])

	rec = serveBranding(f.h, f.asAdmin(newRequest(http.MethodDelete, "/api/v1/branding/logo", "")))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, decodeBranding(t, rec).Logo)
	require.Len(t, f.audit.events, 3)
	assert.Equal(t, "branding.logo_deleted", f.audit.events[2].Type)
	assert.Equal(t, 64, f.audit.events[2].Payload.(map[string]any)["before"].(map[string]any)["width"])
	assert.False(t, f.settings(t).HasLogo())

	assert.Equal(t, http.StatusNotFound, serveBranding(f.h, newRequest(http.MethodGet, "/api/v1/branding/logo", "")).Code)
	assert.Equal(t, http.StatusNotFound, serveBranding(f.h, f.asAdmin(newRequest(http.MethodDelete, "/api/v1/branding/logo", ""))).Code)
	assert.Len(t, f.audit.events, 3, "deleting a missing logo records nothing")

	testutil.RequireNoIdentifyingLabels(t)
}

func TestPostBrandingLogoRejectsAndStoresNothing(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)

	valid := pngBytes(t, 64, 64)
	corrupt := append([]byte{}, valid[:len(valid)-20]...)
	corrupt = append(corrupt, make([]byte, 20)...)
	// Over the 512 KB file cap but under the 1 MB body cap.
	oversize := append(append([]byte{}, valid[:8]...), make([]byte, branding.MaxLogoBytes)...)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)

	cases := []struct {
		name   string
		req    *http.Request
		status int
		path   string
		code   string
	}{
		{"jpeg renamed png", logoRequest(t, "logo.png", "image/png", jpeg, strPtr("x")), 422, "file", "unsupported_type"},
		{"svg renamed png", logoRequest(t, "logo.png", "image/png", svg, strPtr("x")), 422, "file", "unsupported_type"},
		{"corrupt body", logoRequest(t, "logo.png", "image/png", corrupt, strPtr("x")), 422, "file", "invalid_image"},
		{"over 512 KB", logoRequest(t, "logo.png", "image/png", oversize, strPtr("x")), 422, "file", "too_large"},
		{"too small", logoRequest(t, "logo.png", "image/png", pngBytes(t, 8, 8), strPtr("x")), 422, "file", "invalid_dimensions"},
		{"too wide", logoRequest(t, "logo.png", "image/png", pngBytes(t, 2001, 20), strPtr("x")), 422, "file", "invalid_dimensions"},
		{"empty file", logoRequest(t, "logo.png", "image/png", []byte{}, strPtr("x")), 422, "file", "required"},
		{"no file", logoRequest(t, "", "", nil, strPtr("x")), 422, "file", "required"},
		{"no alt", logoRequest(t, "logo.png", "image/png", valid, nil), 422, "alt", "invalid"},
		{"blank alt", logoRequest(t, "logo.png", "image/png", valid, strPtr(" \n ")), 422, "alt", "invalid"},
		{"long alt", logoRequest(t, "logo.png", "image/png", valid, strPtr(strings.Repeat("a", 256))), 422, "alt", "invalid"},
		{"body over 1 MB", logoRequest(t, "logo.png", "image/png", make([]byte, 1<<20+1), strPtr("x")), 413, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveBranding(f.h, f.asAdmin(tc.req))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.path == "" {
				return
			}
			e := decodeError(t, rec).Error
			require.Len(t, e.Errors, 1)
			assert.Equal(t, tc.path, e.Errors[0].Path)
			assert.Equal(t, tc.code, e.Errors[0].Code)
		})
	}

	assert.False(t, f.settings(t).HasLogo(), "nothing is stored")
	assert.Empty(t, f.audit.events)
}

// A principal with no users row (dev mode) still saves; the author column
// stays NULL instead of failing the foreign key.
func TestBrandingWriteWithoutUserRow(t *testing.T) {
	t.Parallel()
	f := newBrandingFixture(t)
	req := asPrincipal(newRequest(http.MethodPut, "/api/v1/branding", customPalette), principal(testUser(), models.RolePlatformAdmin))
	require.Equal(t, http.StatusOK, serveBranding(f.h, req).Code)
	assert.Nil(t, f.settings(t).UpdatedByUserID)
}
