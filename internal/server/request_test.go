package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stut/imgsrv/internal/token"
)

var testDims = []int{200, 400, 800, 1600, 600}

func TestParseRequest(t *testing.T) {
	cases := []struct {
		path     string
		dir      string
		basename string
		tok      string
		format   Format
	}{
		{"/holiday/photo-400x400.webp", "holiday", "photo", "400", FormatWebP},
		{"/holiday/photo-400.webp", "holiday", "photo", "400", FormatWebP},
		{"/photo-400w.jpeg", "", "photo", "400w", FormatJPEG},
		{"/photo-400w.jpg", "", "photo", "400w", FormatJPEG},
		{"/a/b/c/photo-800.avif", "a/b/c", "photo", "800", FormatAVIF},
		{"/my-photo-400w.webp", "", "my-photo", "400w", FormatWebP},
		{"/holiday/photo-original.webp", "holiday", "photo", "original", FormatWebP},
		{"/holiday/photo-1600x600z.webp", "holiday", "photo", "1600x600z", FormatWebP},
	}
	for _, c := range cases {
		req, err := ParseRequest(c.path, testDims)
		if err != nil {
			t.Errorf("ParseRequest(%q): %v", c.path, err)
			continue
		}
		if req.Dir != c.dir || req.Basename != c.basename || req.Token.String() != c.tok || req.Format != c.format {
			t.Errorf("ParseRequest(%q) = %+v", c.path, req)
		}
	}
}

func TestParseRequestErrors(t *testing.T) {
	cases := map[string]string{
		"no token":              "/holiday/photo.webp",
		"empty basename":        "/holiday/-400.webp",
		"bad token":             "/holiday/photo-4x4x4.webp",
		"non-allowlisted dim":   "/holiday/photo-500.webp",
		"unsupported extension": "/holiday/photo-400.png",
		"no extension":          "/holiday/photo-400",
		"traversal":             "/../originals/photo-400.webp",
		"dot dir":               "/.hidden/photo-400.webp",
		"dotfile":               "/holiday/.photo-400.webp",
		"trailing slash":        "/holiday/photo-400.webp/",
		"double slash":          "//holiday/photo-400.webp",
		"transparent pad jpeg":  "/holiday/photo-400x400t.jpeg",
		"relative":              "holiday/photo-400.webp",
	}
	for name, path := range cases {
		if req, err := ParseRequest(path, testDims); err == nil {
			t.Errorf("%s: ParseRequest(%q) = %+v, want error", name, path, req)
		}
	}
}

func TestParseRequestTransparentPadWebPAllowed(t *testing.T) {
	req, err := ParseRequest("/photo-400x400t.webp", testDims)
	if err != nil {
		t.Fatal(err)
	}
	if req.Token.Mode != token.ModePadTransparent {
		t.Errorf("mode = %v", req.Token.Mode)
	}
}

func TestCachePath(t *testing.T) {
	req, err := ParseRequest("/holiday/photo-400.webp", testDims)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/cache", "holiday", "photo-400.webp")
	if got := req.CachePath("/cache"); got != want {
		t.Errorf("CachePath = %q, want %q", got, want)
	}
}

func TestResolveOriginal(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "holiday/photo.jpg")
	mkfile(t, root, "holiday/both.png")
	mkfile(t, root, "holiday/both.jpg")

	exts := []string{"jpg", "jpeg", "png", "tif", "tiff"}

	req := Request{Dir: "holiday", Basename: "photo"}
	got, err := ResolveOriginal(root, req, exts)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "holiday", "photo.jpg"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Priority order breaks basename ties: jpg beats png.
	got, err = ResolveOriginal(root, Request{Dir: "holiday", Basename: "both"}, exts)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "holiday", "both.jpg"); got != want {
		t.Errorf("tie-break: got %q, want %q", got, want)
	}

	// Missing original → os.ErrNotExist.
	_, err = ResolveOriginal(root, Request{Dir: "holiday", Basename: "nope"}, exts)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing original: err = %v, want ErrNotExist", err)
	}

	// Escape attempts are refused.
	for _, req := range []Request{
		{Dir: "..", Basename: "photo"},
		{Dir: "holiday/../..", Basename: "photo"},
		{Dir: "holiday", Basename: "../photo"},
	} {
		if p, err := ResolveOriginal(root, req, exts); err == nil {
			t.Errorf("escape %+v resolved to %q, want error", req, p)
		}
	}
}

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
}
