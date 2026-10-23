// Package branding turns the six colours a platform admin sets into the token
// set the webapp renders, checks the palette against WCAG AA, and validates
// an uploaded logo. Nothing here touches the database or HTTP.
//
// Six inputs, not one per token: an operator knows their organisation's brand
// hexes but not what a good hover or active state looks like, and six inputs
// cannot produce an incoherent interaction system.
//
// The default palette is the one the webapp's stylesheet defines
// (Webapp/assets/css/govnl.css, the --brand-* block). DefaultTokens returns
// those values verbatim, so an untouched install renders exactly as before;
// a custom palette gets shades derived with the factors below.
package branding

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// RGB is an 8-bit-per-channel colour.
type RGB struct{ R, G, B uint8 }

// Palette is the six colours an operator sets directly.
type Palette struct {
	CTA       RGB
	Primary   RGB
	Secondary RGB
	Surface   RGB
	BodyText  RGB
	Border    RGB
}

// Tokens is the resolved set the webapp writes into its --brand-* custom
// properties. Every field is a lowercase #rrggbb value, including the three
// hero-wedge gradient stops, so the webapp can reject anything else before it
// reaches a stylesheet.
type Tokens struct {
	CTA       string `json:"cta"`
	CTAHover  string `json:"cta_hover"`
	CTAActive string `json:"cta_active"`
	CTASubtle string `json:"cta_subtle"`

	Primary       string `json:"primary"`
	PrimaryHover  string `json:"primary_hover"`
	PrimaryActive string `json:"primary_active"`
	PrimarySubtle string `json:"primary_subtle"`

	Secondary string `json:"secondary"`
	Surface   string `json:"surface"`
	BodyText  string `json:"body_text"`
	Border    string `json:"border"`

	WedgeStart string `json:"wedge_start"`
	WedgeMid   string `json:"wedge_mid"`
	WedgeEnd   string `json:"wedge_end"`
}

// Derivation factors for a custom palette. Hover and active darken every
// channel; subtle moves the colour 85% of the way to white, which is how
// lintblauw-50 relates to lintblauw-500. The wedge runs from the active shade
// through Primary to a lighter Primary, the shape of the default gradient.
const (
	hoverFactor      = 0.80
	activeFactor     = 0.60
	subtleMix        = 0.85
	wedgeLightFactor = 1.26
)

var hexPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// ParseHex accepts "#RRGGBB" in either case and returns the colour. Short
// form, alpha and named colours are rejected: the columns are varchar(7) and
// the webapp only ever writes #rrggbb into its stylesheet.
func ParseHex(s string) (RGB, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if !hexPattern.MatchString(v) {
		return RGB{}, fmt.Errorf("branding: %q is not a #RRGGBB colour", s)
	}
	var c RGB
	// The pattern guarantees six hex digits, so this cannot fail.
	_, _ = fmt.Sscanf(v[1:], "%02x%02x%02x", &c.R, &c.G, &c.B)
	return c, nil
}

// Hex renders a colour as lowercase "#rrggbb", the form stored and served.
func (c RGB) Hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func (c RGB) scale(f float64) RGB {
	return RGB{R: clamp(float64(c.R) * f), G: clamp(float64(c.G) * f), B: clamp(float64(c.B) * f)}
}

func (c RGB) mixToWhite(t float64) RGB {
	return RGB{
		R: clamp(float64(c.R) + (255-float64(c.R))*t),
		G: clamp(float64(c.G) + (255-float64(c.G))*t),
		B: clamp(float64(c.B) + (255-float64(c.B))*t),
	}
}

func clamp(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// Default is the GovNL palette: the migration's seed, the reset target and
// the palette the contrast check is calibrated against.
func Default() Palette {
	return Palette{
		CTA:       RGB{0xca, 0x00, 0x5d}, // robijnrood-500
		Primary:   RGB{0x15, 0x42, 0x73}, // lintblauw-500
		Secondary: RGB{0xe1, 0xf0, 0xfa}, // hemelblauw-50
		Surface:   RGB{0xf1, 0xf5, 0xf9}, // grijs-100
		BodyText:  RGB{0x0f, 0x17, 0x2a}, // grijs-900
		Border:    RGB{0xcb, 0xd5, 0xe1}, // grijs-300
	}
}

// IsDefault reports whether p is the untouched GovNL palette.
func IsDefault(p Palette) bool { return p == Default() }

// DefaultTokens is the --brand-* block of govnl.css, verbatim. The GovNL
// ramps were hand-picked, so the derivation factors come close to these
// shades without reproducing them; serving the literals keeps an untouched
// install identical.
func DefaultTokens() Tokens {
	return Tokens{
		CTA:       "#ca005d",
		CTAHover:  "#a30049",
		CTAActive: "#7d0038",
		CTASubtle: "#f6dde7",

		Primary:       "#154273",
		PrimaryHover:  "#11355c",
		PrimaryActive: "#0d2845",
		PrimarySubtle: "#dce3ea",

		Secondary: "#e1f0fa",
		Surface:   "#f1f5f9",
		BodyText:  "#0f172a",
		Border:    "#cbd5e1",

		WedgeStart: "#0a2750",
		WedgeMid:   "#154273",
		WedgeEnd:   "#1b5391",
	}
}

// Derive computes the token set for p. Only CTA and Primary get hover, active
// and subtle shades; the other four are used flat.
func Derive(p Palette) Tokens {
	if IsDefault(p) {
		return DefaultTokens()
	}
	return Tokens{
		CTA:       p.CTA.Hex(),
		CTAHover:  p.CTA.scale(hoverFactor).Hex(),
		CTAActive: p.CTA.scale(activeFactor).Hex(),
		CTASubtle: p.CTA.mixToWhite(subtleMix).Hex(),

		Primary:       p.Primary.Hex(),
		PrimaryHover:  p.Primary.scale(hoverFactor).Hex(),
		PrimaryActive: p.Primary.scale(activeFactor).Hex(),
		PrimarySubtle: p.Primary.mixToWhite(subtleMix).Hex(),

		Secondary: p.Secondary.Hex(),
		Surface:   p.Surface.Hex(),
		BodyText:  p.BodyText.Hex(),
		Border:    p.Border.Hex(),

		WedgeStart: p.Primary.scale(activeFactor).Hex(),
		WedgeMid:   p.Primary.Hex(),
		WedgeEnd:   p.Primary.scale(wedgeLightFactor).Hex(),
	}
}

// AANormalText is the WCAG 2.2 SC 1.4.3 minimum for normal-size text.
const AANormalText = 4.5

// ContrastFailure is one checked text pair below its WCAG AA minimum.
type ContrastFailure struct {
	// Pair is a stable identifier, such as "white_on_cta".
	Pair string `json:"pair"`
	// Foreground and Background are the colours measured, as #rrggbb.
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Ratio      float64 `json:"ratio"`
	Required   float64 `json:"required"`
}

var white = RGB{0xff, 0xff, 0xff}

// Contrast returns every checked text pair in p that falls below AA, in a
// fixed order; an empty slice means the palette passes. The pairs are the
// ones the webapp renders text on: white on the CTA and primary colours
// (and their hover shades), primary as link or heading text on white and on
// the secondary colour, and body text on white and on the surface colour.
//
// Border is not a text colour and is not checked: the default #cbd5e1 is a
// decorative divider at 1.48:1 on white.
func Contrast(p Palette) []ContrastFailure {
	// Measure the tokens actually served, which for the default palette are
	// the stylesheet literals rather than derived shades.
	t := Derive(p)
	ctaHover := mustParse(t.CTAHover)
	primaryHover := mustParse(t.PrimaryHover)

	checks := []struct {
		pair   string
		fg, bg RGB
	}{
		{"white_on_cta", white, p.CTA},
		{"white_on_cta_hover", white, ctaHover},
		{"white_on_primary", white, p.Primary},
		{"white_on_primary_hover", white, primaryHover},
		{"primary_on_white", p.Primary, white},
		{"primary_on_secondary", p.Primary, p.Secondary},
		{"body_text_on_white", p.BodyText, white},
		{"body_text_on_surface", p.BodyText, p.Surface},
	}

	failures := make([]ContrastFailure, 0)
	for _, c := range checks {
		if r := Ratio(c.fg, c.bg); r < AANormalText {
			failures = append(failures, ContrastFailure{
				Pair: c.pair, Foreground: c.fg.Hex(), Background: c.bg.Hex(),
				Ratio: r, Required: AANormalText,
			})
		}
	}
	return failures
}

// mustParse reads a hex value this package produced. A failure would be a bug
// here, not bad input, so it degrades to black instead of panicking.
func mustParse(s string) RGB {
	c, err := ParseHex(s)
	if err != nil {
		return RGB{}
	}
	return c
}

// Ratio is the WCAG contrast ratio between two colours, rounded down to two
// decimals so a pair just under the threshold is never reported as passing.
// The order of the arguments does not matter.
func Ratio(a, b RGB) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return math.Floor((hi+0.05)/(lo+0.05)*100) / 100
}

func relativeLuminance(c RGB) float64 {
	return 0.2126*linearise(c.R) + 0.7152*linearise(c.G) + 0.0722*linearise(c.B)
}

func linearise(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}
