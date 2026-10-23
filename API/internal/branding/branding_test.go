package branding

import (
	"bytes"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHex(t *testing.T) {
	t.Parallel()
	c, err := ParseHex("  #A90061 ")
	require.NoError(t, err)
	assert.Equal(t, RGB{0xa9, 0x00, 0x61}, c)
	assert.Equal(t, "#a90061", c.Hex())

	for _, bad := range []string{"", "a90061", "#a9006", "#a900611", "#fff", "#a90061ff", "red", "#gggggg",
		"#a90061;", "#a90061}", "#a9006<", "rgb(1,2,3)"} {
		_, err := ParseHex(bad)
		assert.Error(t, err, bad)
	}
}

func TestDefaultPaletteDerivesStylesheetTokens(t *testing.T) {
	t.Parallel()
	assert.True(t, IsDefault(Default()))
	assert.Equal(t, DefaultTokens(), Derive(Default()))
	// The six inputs equal the flat tokens they feed.
	d, tok := Default(), DefaultTokens()
	assert.Equal(t, tok.CTA, d.CTA.Hex())
	assert.Equal(t, tok.Primary, d.Primary.Hex())
	assert.Equal(t, tok.Secondary, d.Secondary.Hex())
	assert.Equal(t, tok.Surface, d.Surface.Hex())
	assert.Equal(t, tok.BodyText, d.BodyText.Hex())
	assert.Equal(t, tok.Border, d.Border.Hex())
}

func TestDeriveCustomPalette(t *testing.T) {
	t.Parallel()
	p := Default()
	p.CTA = RGB{100, 50, 0}
	p.Primary = RGB{0, 100, 250}
	assert.False(t, IsDefault(p))

	tok := Derive(p)
	assert.Equal(t, "#643200", tok.CTA)
	assert.Equal(t, "#502800", tok.CTAHover)  // ×0.8
	assert.Equal(t, "#3c1e00", tok.CTAActive) // ×0.6
	assert.Equal(t, "#e8e0d9", tok.CTASubtle) // 85% to white
	assert.Equal(t, "#0050c8", tok.PrimaryHover)
	assert.Equal(t, "#003c96", tok.PrimaryActive)
	assert.Equal(t, "#d9e8fe", tok.PrimarySubtle)
	assert.Equal(t, "#003c96", tok.WedgeStart)
	assert.Equal(t, "#0064fa", tok.WedgeMid)
	assert.Equal(t, "#007eff", tok.WedgeEnd, "the light stop clamps at 255")
}

// Every token is #rrggbb, so the webapp's sanitiser never has to accept
// anything else.
func TestTokensAreAlwaysHex(t *testing.T) {
	t.Parallel()
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for _, p := range []Palette{Default(), {CTA: RGB{255, 255, 255}}, {Primary: RGB{1, 2, 3}}} {
		tok := Derive(p)
		for _, v := range []string{tok.CTA, tok.CTAHover, tok.CTAActive, tok.CTASubtle, tok.Primary,
			tok.PrimaryHover, tok.PrimaryActive, tok.PrimarySubtle, tok.Secondary, tok.Surface,
			tok.BodyText, tok.Border, tok.WedgeStart, tok.WedgeMid, tok.WedgeEnd} {
			assert.Regexp(t, hex, v)
		}
	}
}

func TestDefaultPalettePassesContrast(t *testing.T) {
	t.Parallel()
	assert.Empty(t, Contrast(Default()))
}

func TestContrastReportsFailingPairs(t *testing.T) {
	t.Parallel()
	p := Default()
	p.CTA = RGB{0x00, 0x9d, 0xff} // #009dff from #48, 2.88:1 on white
	failures := Contrast(p)
	require.Len(t, failures, 2)
	f := failures[0]
	assert.Equal(t, "white_on_cta", f.Pair)
	assert.Equal(t, "#ffffff", f.Foreground)
	assert.Equal(t, "#009dff", f.Background)
	assert.InDelta(t, 2.88, f.Ratio, 0)
	assert.InDelta(t, AANormalText, f.Required, 0)
	assert.Equal(t, "white_on_cta_hover", failures[1].Pair)
	assert.Equal(t, "#007ecc", failures[1].Background, "the hover shade is CTA ×0.8")

	p = Default()
	p.BodyText = RGB{0xaa, 0xaa, 0xaa}
	p.Primary = RGB{0xdd, 0xdd, 0xdd}
	pairs := make([]string, 0)
	for _, f := range Contrast(p) {
		pairs = append(pairs, f.Pair)
	}
	assert.Equal(t, []string{
		"white_on_primary", "white_on_primary_hover", "primary_on_white", "primary_on_secondary",
		"body_text_on_white", "body_text_on_surface",
	}, pairs)
}

func TestRatio(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 21.0, Ratio(RGB{}, white), 0)
	assert.InDelta(t, 1.0, Ratio(white, white), 0)
	assert.Equal(t, Ratio(RGB{1, 2, 3}, white), Ratio(white, RGB{1, 2, 3}))
	// #767676 is the classic 4.54:1 grey; rounding down keeps it at 4.54.
	assert.InDelta(t, 4.54, Ratio(RGB{0x76, 0x76, 0x76}, white), 0)
}

func TestMustParseDegradesToBlack(t *testing.T) {
	t.Parallel()
	assert.Equal(t, RGB{}, mustParse("nope"))
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.NRGBA{R: uint8(x), A: 255})
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestValidateLogo(t *testing.T) {
	t.Parallel()
	w, h, err := ValidateLogo(encodePNG(t, 200, 80))
	require.NoError(t, err)
	assert.Equal(t, 200, w)
	assert.Equal(t, 80, h)

	_, _, err = ValidateLogo(encodePNG(t, MinLogoWidth, MinLogoHeight))
	require.NoError(t, err, "the minimum is inclusive")
	_, _, err = ValidateLogo(encodePNG(t, MaxLogoWidth, MaxLogoHeight))
	require.NoError(t, err, "the maximum is inclusive")

	valid := encodePNG(t, 64, 64)
	corrupt := append([]byte{}, valid[:len(valid)-30]...)
	corrupt = append(corrupt, make([]byte, 30)...)

	cases := map[string]struct {
		raw  []byte
		code string
	}{
		"empty":            {nil, "required"},
		"too large":        {append(append([]byte{}, pngSignature...), make([]byte, MaxLogoBytes)...), "too_large"},
		"jpeg":             {[]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, "unsupported_type"},
		"svg":              {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "unsupported_type"},
		"signature only":   {append([]byte{}, pngSignature...), "invalid_image"},
		"corrupt body":     {corrupt, "invalid_image"},
		"too small":        {encodePNG(t, 15, 16), "invalid_dimensions"},
		"too short":        {encodePNG(t, 16, 15), "invalid_dimensions"},
		"too wide":         {encodePNG(t, MaxLogoWidth+1, 16), "invalid_dimensions"},
		"too tall":         {encodePNG(t, 16, MaxLogoHeight+1), "invalid_dimensions"},
		"huge in IHDR":     {hugeIHDR(t), "invalid_dimensions"},
		"truncated header": {valid[:20], "invalid_image"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := ValidateLogo(tc.raw)
			var le *LogoError
			require.True(t, errors.As(err, &le), "got %v", err)
			assert.Equal(t, tc.code, le.Code)
			assert.NotEmpty(t, le.Error())
		})
	}
}

// hugeIHDR is a small file whose header declares 100000×100000 pixels; it
// must be refused before png.Decode tries to allocate the bitmap.
func hugeIHDR(t *testing.T) []byte {
	t.Helper()
	raw := encodePNG(t, 16, 16)
	// IHDR data starts at byte 16: width then height, four bytes each,
	// followed by the chunk CRC over type and data (bytes 12 to 28).
	out := append([]byte{}, raw...)
	putU32(out[16:], 100000)
	putU32(out[20:], 100000)
	crc := crc32.ChecksumIEEE(out[12:29])
	putU32(out[29:], crc)
	return out
}

func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func TestNormaliseLogoAlt(t *testing.T) {
	t.Parallel()
	alt, err := NormaliseLogoAlt("  Gemeente\tVoorbeeld\n\x00logo  ")
	require.NoError(t, err)
	assert.Equal(t, "Gemeente Voorbeeld logo", alt)

	_, err = NormaliseLogoAlt(" \t\n ")
	require.ErrorIs(t, err, ErrLogoAlt)
	_, err = NormaliseLogoAlt(strings.Repeat("é", MaxLogoAltRunes+1))
	require.ErrorIs(t, err, ErrLogoAlt)
	alt, err = NormaliseLogoAlt(strings.Repeat("é", MaxLogoAltRunes))
	require.NoError(t, err)
	assert.Len(t, []rune(alt), MaxLogoAltRunes)
}
