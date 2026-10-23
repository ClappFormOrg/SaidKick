package branding

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"unicode"
)

// Logo limits. PNG only: SVG can carry script, and one raster format keeps
// the served Content-Type a constant.
const (
	LogoContentType = "image/png"
	MaxLogoBytes    = 512 << 10
	MinLogoWidth    = 16
	MinLogoHeight   = 16
	MaxLogoWidth    = 2000
	MaxLogoHeight   = 800
	MaxLogoAltRunes = 255
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// LogoError is a rejected upload. Code is a stable identifier for the API's
// field error; the message is for people.
type LogoError struct {
	Code string
	msg  string
}

func (e *LogoError) Error() string { return e.msg }

func logoErr(code, format string, args ...any) *LogoError {
	return &LogoError{Code: code, msg: fmt.Sprintf(format, args...)}
}

// ValidateLogo checks that raw is a PNG within the size and dimension limits
// and returns its intrinsic size. The declared content type and file name of
// the upload are never trusted: the signature is sniffed, and the image is
// fully decoded so a valid header on a corrupt body is rejected.
//
// DecodeConfig reads only the IHDR chunk, so the dimension check runs before
// the full decode; png.Decode allocates the whole bitmap, and a small file can
// declare a huge one.
func ValidateLogo(raw []byte) (width, height int, err error) {
	switch {
	case len(raw) == 0:
		return 0, 0, logoErr("required", "logo file is empty")
	case len(raw) > MaxLogoBytes:
		return 0, 0, logoErr("too_large", "logo is %d bytes; the maximum is %d", len(raw), MaxLogoBytes)
	case !bytes.HasPrefix(raw, pngSignature):
		return 0, 0, logoErr("unsupported_type", "logo must be a PNG image")
	}

	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, logoErr("invalid_image", "logo is not a readable PNG")
	}
	if cfg.Width < MinLogoWidth || cfg.Height < MinLogoHeight ||
		cfg.Width > MaxLogoWidth || cfg.Height > MaxLogoHeight {
		return 0, 0, logoErr("invalid_dimensions",
			"logo is %dx%d; it must be between %dx%d and %dx%d pixels",
			cfg.Width, cfg.Height, MinLogoWidth, MinLogoHeight, MaxLogoWidth, MaxLogoHeight)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		return 0, 0, logoErr("invalid_image", "logo is not a readable PNG")
	}
	return cfg.Width, cfg.Height, nil
}

// ErrLogoAlt is returned by NormaliseLogoAlt for an empty or overlong text.
var ErrLogoAlt = errors.New("branding: logo alt text must be 1 to 255 characters")

// NormaliseLogoAlt trims the alt text, folds every run of whitespace and
// control characters to one space, and checks its length.
func NormaliseLogoAlt(s string) (string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
	alt := strings.Join(fields, " ")
	if n := len([]rune(alt)); n == 0 || n > MaxLogoAltRunes {
		return "", ErrLogoAlt
	}
	return alt, nil
}
