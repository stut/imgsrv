// Package token implements the size-token grammar used in derivative URLs.
//
//	token := size mod? | "original"
//	size  := N | Nw | Nh | WxH
//	mod   := f | z | s | t | b | w | g
//
// Mod letters never collide with size syntax: `w`/`h` bind to a bare number
// as a dimension selector (400w = width 400), while mod letters are only
// valid after a complete box (bare N, which is shorthand for NxN, or WxH).
package token

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Mode is the resize/crop/pad behaviour requested by the mod letter.
type Mode int

const (
	// ModeFit fits inside the box; output at the fitted dimensions. Default.
	ModeFit Mode = iota
	// ModeFill covers the box and centre-crops to exactly WxH (`z`).
	ModeFill
	// ModeSmart is like ModeFill but crops around the detected focal point (`s`).
	ModeSmart
	// ModePadTransparent fits inside the box on an exactly-WxH transparent canvas (`t`).
	ModePadTransparent
	// ModePadBlack pads with a black background (`b`).
	ModePadBlack
	// ModePadWhite pads with a white background (`w`).
	ModePadWhite
	// ModePadGrey pads with a 50% grey background (`g`).
	ModePadGrey
)

// IsPad reports whether the mode fits the image inside the box on an
// exactly-sized canvas with a background.
func (m Mode) IsPad() bool {
	switch m {
	case ModePadTransparent, ModePadBlack, ModePadWhite, ModePadGrey:
		return true
	}
	return false
}

// Token is a parsed size token.
type Token struct {
	// Original is true for the literal "original" token: native dimensions,
	// full re-encode pipeline.
	Original bool
	// Width and Height define the box. For width-only (Nw) Height is 0;
	// for height-only (Nh) Width is 0. Bare N sets both to N.
	Width, Height int
	Mode          Mode
}

// String reconstructs the canonical token text.
func (t Token) String() string {
	if t.Original {
		return "original"
	}
	var size string
	switch {
	case t.Height == 0:
		size = fmt.Sprintf("%dw", t.Width)
	case t.Width == 0:
		size = fmt.Sprintf("%dh", t.Height)
	case t.Width == t.Height:
		size = strconv.Itoa(t.Width)
	default:
		size = fmt.Sprintf("%dx%d", t.Width, t.Height)
	}
	return size + modSuffix(t.Mode)
}

func modSuffix(m Mode) string {
	switch m {
	case ModeFill:
		return "z"
	case ModeSmart:
		return "s"
	case ModePadTransparent:
		return "t"
	case ModePadBlack:
		return "b"
	case ModePadWhite:
		return "w"
	case ModePadGrey:
		return "g"
	}
	return ""
}

func modeFor(letter byte) (Mode, bool) {
	switch letter {
	case 'f':
		return ModeFit, true
	case 'z':
		return ModeFill, true
	case 's':
		return ModeSmart, true
	case 't':
		return ModePadTransparent, true
	case 'b':
		return ModePadBlack, true
	case 'w':
		return ModePadWhite, true
	case 'g':
		return ModePadGrey, true
	}
	return 0, false
}

// Parse parses a size token. It returns an error for anything that doesn't
// match the grammar; allowlist validation is the caller's job.
func Parse(s string) (Token, error) {
	if s == "original" {
		return Token{Original: true}, nil
	}
	if s == "" {
		return Token{}, fmt.Errorf("empty token")
	}

	if w, h, ok := strings.Cut(s, "x"); ok {
		// WxH with optional mod letter on the height part.
		width, err := parseDim(w)
		if err != nil {
			return Token{}, fmt.Errorf("token %q: %w", s, err)
		}
		mode := ModeFit
		if n := len(h); n > 0 && !isDigit(h[n-1]) {
			m, ok := modeFor(h[n-1])
			if !ok {
				return Token{}, fmt.Errorf("token %q: unknown mod %q", s, h[n-1])
			}
			mode, h = m, h[:n-1]
		}
		height, err := parseDim(h)
		if err != nil {
			return Token{}, fmt.Errorf("token %q: %w", s, err)
		}
		return Token{Width: width, Height: height, Mode: mode}, nil
	}

	// Bare number, dimension selector (w/h), or bare number + mod.
	num := s
	var suffix byte
	if n := len(s); !isDigit(s[n-1]) {
		suffix, num = s[n-1], s[:n-1]
	}
	d, err := parseDim(num)
	if err != nil {
		return Token{}, fmt.Errorf("token %q: %w", s, err)
	}
	switch suffix {
	case 0:
		return Token{Width: d, Height: d}, nil
	case 'w':
		return Token{Width: d}, nil
	case 'h':
		return Token{Height: d}, nil
	}
	if m, ok := modeFor(suffix); ok {
		return Token{Width: d, Height: d, Mode: m}, nil
	}
	return Token{}, fmt.Errorf("token %q: unknown mod %q", s, suffix)
}

func parseDim(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("missing dimension")
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return 0, fmt.Errorf("invalid dimension %q", s)
		}
	}
	d, err := strconv.Atoi(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid dimension %q", s)
	}
	return d, nil
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Validate checks the token's dimensions against the allowlist. Every
// dimension number present in the token must appear in allowed.
func (t Token) Validate(allowed []int) error {
	if t.Original {
		return nil
	}
	for _, d := range []int{t.Width, t.Height} {
		if d == 0 {
			continue
		}
		if !slices.Contains(allowed, d) {
			return fmt.Errorf("dimension %d not allowlisted", d)
		}
	}
	return nil
}
