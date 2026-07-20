package processor

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/stut/imgsrv/internal/server"
	"github.com/stut/imgsrv/internal/token"
)

func TestMain(m *testing.M) {
	shutdown, err := Startup(slog.Default())
	if err != nil {
		panic(err)
	}
	code := m.Run()
	shutdown()
	os.Exit(code)
}

// writeOriginal writes a landscape 300x200 PNG with distinct edge colours so
// crops are detectable.
func writeOriginal(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := range 200 {
		for x := range 300 {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y), B: 100, A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "photo.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return path
}

func process(t *testing.T, tok string, format server.Format) *vips.ImageRef {
	t.Helper()
	tk, err := token.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	src := writeOriginal(t)
	dst := filepath.Join(t.TempDir(), "out."+string(format))
	if err := New().Process(context.Background(), src, dst, tk, format, 80); err != nil {
		t.Fatalf("Process(%s, %s): %v", tok, format, err)
	}
	out, err := vips.NewImageFromFile(dst)
	if err != nil {
		t.Fatalf("loading output: %v", err)
	}
	t.Cleanup(out.Close)
	return out
}

func TestFit(t *testing.T) {
	out := process(t, "100", server.FormatWebP)
	// 300x200 fitted inside 100x100 → 100 wide, ~67 high, no canvas.
	if out.Width() != 100 || out.Height() < 66 || out.Height() > 67 {
		t.Errorf("dims = %dx%d, want 100x~67", out.Width(), out.Height())
	}
}

func TestFitWidthOnly(t *testing.T) {
	out := process(t, "150w", server.FormatWebP)
	if out.Width() != 150 || out.Height() != 100 {
		t.Errorf("dims = %dx%d, want 150x100", out.Width(), out.Height())
	}
}

func TestFitHeightOnly(t *testing.T) {
	out := process(t, "100h", server.FormatWebP)
	if out.Height() != 100 || out.Width() != 150 {
		t.Errorf("dims = %dx%d, want 150x100", out.Width(), out.Height())
	}
}

func TestNoUpscale(t *testing.T) {
	out := process(t, "800", server.FormatWebP)
	if out.Width() != 300 || out.Height() != 200 {
		t.Errorf("dims = %dx%d, want original 300x200 (no upscaling)", out.Width(), out.Height())
	}
}

func TestFillCentreCrop(t *testing.T) {
	out := process(t, "100x100z", server.FormatWebP)
	if out.Width() != 100 || out.Height() != 100 {
		t.Errorf("dims = %dx%d, want exactly 100x100", out.Width(), out.Height())
	}
}

func TestSmartCrop(t *testing.T) {
	out := process(t, "100x100s", server.FormatWebP)
	if out.Width() != 100 || out.Height() != 100 {
		t.Errorf("dims = %dx%d, want exactly 100x100", out.Width(), out.Height())
	}
}

func TestPadTransparent(t *testing.T) {
	out := process(t, "100x100t", server.FormatWebP)
	if out.Width() != 100 || out.Height() != 100 {
		t.Errorf("dims = %dx%d, want exactly 100x100", out.Width(), out.Height())
	}
	if !out.HasAlpha() {
		t.Error("transparent pad output has no alpha channel")
	}
}

func TestPadBlackJpeg(t *testing.T) {
	out := process(t, "100x100b", server.FormatJPEG)
	if out.Width() != 100 || out.Height() != 100 {
		t.Errorf("dims = %dx%d, want exactly 100x100", out.Width(), out.Height())
	}
}

func TestPadNoUpscale(t *testing.T) {
	// Original smaller than the canvas: padded within exactly WxH, image
	// kept at its own size.
	out := process(t, "400x400b", server.FormatWebP)
	if out.Width() != 400 || out.Height() != 400 {
		t.Errorf("dims = %dx%d, want exactly 400x400", out.Width(), out.Height())
	}
}

func TestOriginalToken(t *testing.T) {
	out := process(t, "original", server.FormatWebP)
	if out.Width() != 300 || out.Height() != 200 {
		t.Errorf("dims = %dx%d, want 300x200", out.Width(), out.Height())
	}
}

func TestOutputFormats(t *testing.T) {
	for format, want := range map[server.Format]vips.ImageType{
		server.FormatWebP: vips.ImageTypeWEBP,
		server.FormatJPEG: vips.ImageTypeJPEG,
		server.FormatAVIF: vips.ImageTypeAVIF,
	} {
		out := process(t, "100", format)
		if out.Format() != want {
			t.Errorf("%s: output format = %v, want %v", format, out.Format(), want)
		}
	}
}

func TestMetadataStripped(t *testing.T) {
	out := process(t, "100", server.FormatJPEG)
	if fields := out.GetFields(); len(fields) > 0 {
		for _, f := range fields {
			switch f {
			case "exif-data", "icc-profile-data", "xmp-data", "iptc-data":
				t.Errorf("metadata field %q survived stripping", f)
			}
		}
	}
}
