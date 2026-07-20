// Package processor implements the image pipeline on libvips via govips.
//
// Pipeline order matters: orient → resize → sRGB transform → strip → encode.
// EXIF orientation must be applied before metadata is stripped, and the ICC
// transform to sRGB must happen before the profile is dropped, or wide-gamut
// originals (Adobe RGB, ProPhoto) wash out.
package processor

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/stut/imgsrv/internal/server"
	"github.com/stut/imgsrv/internal/token"
)

// Startup initialises libvips. Call once at process start; the returned
// function shuts libvips down.
func Startup(log *slog.Logger) (func(), error) {
	vips.LoggingSettings(func(messageDomain string, messageLevel vips.LogLevel, message string) {
		if messageLevel <= vips.LogLevelWarning {
			log.Warn(message, "domain", messageDomain)
		}
	}, vips.LogLevelWarning)
	if err := vips.Startup(nil); err != nil {
		return nil, err
	}
	return vips.Shutdown, nil
}

// Processor generates derivatives with libvips.
type Processor struct{}

// New returns a Processor. Startup must have been called first.
func New() *Processor { return &Processor{} }

// Process implements server.Processor.
func (p *Processor) Process(ctx context.Context, srcPath, dstPath string, tok token.Token, format server.Format, quality int) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	params := vips.NewImportParams()
	params.AutoRotate.Set(true) // orient upright before the EXIF tag is stripped
	img, err := vips.LoadImageFromFile(srcPath, params)
	if err != nil {
		return fmt.Errorf("loading %s: %w", srcPath, err)
	}
	defer img.Close()

	if !tok.Original {
		if err := resize(img, tok); err != nil {
			return err
		}
	}

	// Convert wide-gamut originals to sRGB before the profile is stripped.
	if err := img.TransformICCProfile(vips.SRGBV2MicroICCProfilePath); err != nil {
		return fmt.Errorf("sRGB transform: %w", err)
	}
	if err := img.RemoveMetadata(); err != nil {
		return fmt.Errorf("stripping metadata: %w", err)
	}

	buf, err := encode(img, format, quality)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, buf, 0o644)
}

// resize applies the size token. Upscaling never happens: an original
// smaller than the box comes back at its own size (fit), padded without
// enlargement (pad modes), or cropped only if genuinely larger (fill).
func resize(img *vips.ImageRef, tok token.Token) error {
	w, h := img.Width(), img.Height()

	switch tok.Mode {
	case token.ModeFill, token.ModeSmart:
		// Cover the box, then crop to exactly the box (bounded by the
		// original's own size so we never upscale).
		scale := max2(ratio(tok.Width, w), ratio(tok.Height, h))
		if scale < 1 {
			if err := img.Resize(scale, vips.KernelLanczos3); err != nil {
				return fmt.Errorf("resize: %w", err)
			}
		}
		cw, ch := min2(tok.Width, img.Width()), min2(tok.Height, img.Height())
		if cw < img.Width() || ch < img.Height() {
			if tok.Mode == token.ModeSmart {
				if err := img.SmartCrop(cw, ch, vips.InterestingAttention); err != nil {
					return fmt.Errorf("smartcrop: %w", err)
				}
			} else {
				left := (img.Width() - cw) / 2
				top := (img.Height() - ch) / 2
				if err := img.ExtractArea(left, top, cw, ch); err != nil {
					return fmt.Errorf("crop: %w", err)
				}
			}
		}
		return nil

	default: // fit and pad modes both start by fitting inside the box
		scale := fitScale(tok, w, h)
		if scale < 1 {
			if err := img.Resize(scale, vips.KernelLanczos3); err != nil {
				return fmt.Errorf("resize: %w", err)
			}
		}
		if tok.Mode.IsPad() {
			return pad(img, tok)
		}
		return nil
	}
}

// fitScale returns the scale that fits (w, h) inside the token's box,
// capped at 1 (no upscaling). Width-only and height-only tokens constrain
// a single edge.
func fitScale(tok token.Token, w, h int) float64 {
	scale := 1.0
	if tok.Width > 0 {
		scale = min2f(scale, ratio(tok.Width, w))
	}
	if tok.Height > 0 {
		scale = min2f(scale, ratio(tok.Height, h))
	}
	return scale
}

// pad centres the fitted image on an exactly WxH canvas.
func pad(img *vips.ImageRef, tok token.Token) error {
	left := (tok.Width - img.Width()) / 2
	top := (tok.Height - img.Height()) / 2
	if tok.Mode == token.ModePadTransparent {
		if !img.HasAlpha() {
			if err := img.AddAlpha(); err != nil {
				return fmt.Errorf("adding alpha: %w", err)
			}
		}
		if err := img.EmbedBackgroundRGBA(left, top, tok.Width, tok.Height, &vips.ColorRGBA{R: 0, G: 0, B: 0, A: 0}); err != nil {
			return fmt.Errorf("pad: %w", err)
		}
		return nil
	}
	var c vips.Color
	switch tok.Mode {
	case token.ModePadWhite:
		c = vips.Color{R: 255, G: 255, B: 255}
	case token.ModePadGrey:
		c = vips.Color{R: 128, G: 128, B: 128}
	default: // black
		c = vips.Color{R: 0, G: 0, B: 0}
	}
	// Flatten first so alpha in the source composes onto the pad colour
	// rather than surviving over an opaque background.
	if img.HasAlpha() {
		if err := img.Flatten(&c); err != nil {
			return fmt.Errorf("flatten: %w", err)
		}
	}
	if err := img.EmbedBackground(left, top, tok.Width, tok.Height, &c); err != nil {
		return fmt.Errorf("pad: %w", err)
	}
	return nil
}

func encode(img *vips.ImageRef, format server.Format, quality int) ([]byte, error) {
	switch format {
	case server.FormatWebP:
		p := vips.NewWebpExportParams()
		p.Quality = quality
		p.StripMetadata = true
		buf, _, err := img.ExportWebp(p)
		return buf, err
	case server.FormatJPEG:
		p := vips.NewJpegExportParams()
		p.Quality = quality
		p.StripMetadata = true
		buf, _, err := img.ExportJpeg(p)
		return buf, err
	case server.FormatAVIF:
		p := vips.NewAvifExportParams()
		p.Quality = quality
		p.StripMetadata = true
		buf, _, err := img.ExportAvif(p)
		return buf, err
	}
	return nil, fmt.Errorf("unsupported format %q", format)
}

func ratio(target, actual int) float64 {
	if actual <= 0 {
		return 1
	}
	return float64(target) / float64(actual)
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func min2f(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
