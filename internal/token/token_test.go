package token

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Token
	}{
		{"400", Token{Width: 400, Height: 400}},
		{"400w", Token{Width: 400}},
		{"400h", Token{Height: 400}},
		{"1600x600", Token{Width: 1600, Height: 600}},
		{"400f", Token{Width: 400, Height: 400, Mode: ModeFit}},
		{"400z", Token{Width: 400, Height: 400, Mode: ModeFill}},
		{"400t", Token{Width: 400, Height: 400, Mode: ModePadTransparent}},
		{"1600x600f", Token{Width: 1600, Height: 600, Mode: ModeFit}},
		{"1600x600z", Token{Width: 1600, Height: 600, Mode: ModeFill}},
		{"1600x600s", Token{Width: 1600, Height: 600, Mode: ModeSmart}},
		{"400x400t", Token{Width: 400, Height: 400, Mode: ModePadTransparent}},
		{"400x400b", Token{Width: 400, Height: 400, Mode: ModePadBlack}},
		{"400x400w", Token{Width: 400, Height: 400, Mode: ModePadWhite}},
		{"400x400g", Token{Width: 400, Height: 400, Mode: ModePadGrey}},
		{"original", Token{Original: true}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"", "abc", "400x", "x400", "400x400x400", "400q", "400x400q",
		"0", "0x400", "400x0", "-400", "4 00", "400X400", "Original",
		"originals", "400wz", "400hz", "400ww", "400.5", "１００",
	} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", in, got)
		}
	}
}

func TestValidate(t *testing.T) {
	allowed := []int{200, 400, 800, 1600, 600}
	valid := []string{"400", "400w", "600h", "1600x600z", "400x400t", "original"}
	for _, in := range valid {
		tok, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if err := tok.Validate(allowed); err != nil {
			t.Errorf("Validate(%q): unexpected error: %v", in, err)
		}
	}
	invalid := []string{"500", "500w", "400x500", "500x400z"}
	for _, in := range invalid {
		tok, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if err := tok.Validate(allowed); err == nil {
			t.Errorf("Validate(%q): want error, got nil", in)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[string]string{
		"400":       "400",
		"400w":      "400w",
		"400h":      "400h",
		"1600x600z": "1600x600z",
		"400x400t":  "400t", // square boxes canonicalize to the short form
		"original":  "original",
		"400z":      "400z",
	}
	for in, want := range cases {
		tok, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := tok.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, got, want)
		}
	}
}
