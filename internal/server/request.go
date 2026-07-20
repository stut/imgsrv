package server

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/stut/imgsrv/internal/token"
)

// Format is a supported output format.
type Format string

const (
	FormatWebP Format = "webp"
	FormatJPEG Format = "jpeg"
	FormatAVIF Format = "avif"
)

// ContentType returns the MIME type for the format.
func (f Format) ContentType() string {
	switch f {
	case FormatWebP:
		return "image/webp"
	case FormatJPEG:
		return "image/jpeg"
	case FormatAVIF:
		return "image/avif"
	}
	return "application/octet-stream"
}

// SupportsAlpha reports whether the format can carry an alpha channel.
func (f Format) SupportsAlpha() bool { return f != FormatJPEG }

func formatForExtension(ext string) (Format, bool) {
	switch ext {
	case "webp":
		return FormatWebP, true
	case "jpg", "jpeg":
		return FormatJPEG, true
	case "avif":
		return FormatAVIF, true
	}
	return "", false
}

// Request is a parsed and validated derivative request.
type Request struct {
	// URLPath is the cleaned request path, e.g. "/holiday/photo-400w.webp".
	URLPath string
	// Dir is the directory part relative to the roots, e.g. "holiday".
	Dir string
	// Basename is the original's basename, e.g. "photo".
	Basename string
	Token    token.Token
	Format   Format
}

// CachePath returns the derivative's absolute path under cacheRoot.
// The URL path is the cache key.
func (r Request) CachePath(cacheRoot string) string {
	return filepath.Join(cacheRoot, filepath.FromSlash(strings.TrimPrefix(r.URLPath, "/")))
}

// ParseRequest parses a URL path into a derivative request. Any deviation
// from the grammar or the allowlist is an error (HTTP 400).
func ParseRequest(urlPath string, dimensions []int) (Request, error) {
	if !strings.HasPrefix(urlPath, "/") {
		return Request{}, fmt.Errorf("path must be absolute")
	}
	clean := path.Clean(urlPath)
	if clean != urlPath {
		// Rejecting rather than normalising keeps the URL↔cache-path mapping
		// one-to-one and kills ".." traversal in one move.
		return Request{}, fmt.Errorf("non-canonical path")
	}
	dir, file := path.Split(clean)
	dir = strings.Trim(dir, "/")
	if strings.HasPrefix(file, ".") || strings.Contains(dir, "/.") || strings.HasPrefix(dir, ".") {
		return Request{}, fmt.Errorf("dot segments not allowed")
	}

	stem, ext, ok := cutLast(file, ".")
	if !ok || ext == "" {
		return Request{}, fmt.Errorf("missing extension")
	}
	format, ok := formatForExtension(strings.ToLower(ext))
	if !ok {
		return Request{}, fmt.Errorf("unsupported output format %q", ext)
	}

	// Everything from the last dash to the extension is the size token;
	// what precedes the dash is the original's basename.
	base, tok, ok := cutLast(stem, "-")
	if !ok || base == "" {
		return Request{}, fmt.Errorf("missing size token")
	}
	t, err := token.Parse(tok)
	if err != nil {
		return Request{}, err
	}
	if err := t.Validate(dimensions); err != nil {
		return Request{}, err
	}
	if t.Mode == token.ModePadTransparent && !format.SupportsAlpha() {
		return Request{}, fmt.Errorf("transparent padding is invalid for %s output", format)
	}

	return Request{URLPath: clean, Dir: dir, Basename: base, Token: t, Format: format}, nil
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// ResolveOriginal locates the original for the request under originalsRoot by
// trying each input extension in priority order. It returns os.ErrNotExist if
// no original matches, and refuses any path that escapes originalsRoot.
func ResolveOriginal(originalsRoot string, req Request, inputExtensions []string) (string, error) {
	root, err := filepath.Abs(originalsRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, filepath.FromSlash(req.Dir))
	if !contained(root, dir) {
		return "", fmt.Errorf("path escapes originals root")
	}
	for _, ext := range inputExtensions {
		p := filepath.Join(dir, req.Basename+"."+ext)
		if !contained(root, p) {
			return "", fmt.Errorf("path escapes originals root")
		}
		info, err := os.Stat(p)
		if err == nil && info.Mode().IsRegular() {
			return p, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

func contained(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
