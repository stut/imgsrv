package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stut/imgsrv/internal/config"
	"github.com/stut/imgsrv/internal/token"
)

// stubProcessor writes a marker payload instead of a real image.
type stubProcessor struct {
	calls atomic.Int64
	block chan struct{} // if non-nil, Process waits until closed
	fail  bool
}

func (p *stubProcessor) Process(ctx context.Context, srcPath, dstPath string, tok token.Token, format Format, quality int) error {
	p.calls.Add(1)
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.fail {
		return fmt.Errorf("boom")
	}
	return os.WriteFile(dstPath, fmt.Appendf(nil, "processed %s %s q%d", tok, format, quality), 0o644)
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Dimensions = []int{200, 400, 800, 1600, 600}
	return cfg
}

func newTestServer(t *testing.T, proc Processor) (*Server, string, string) {
	t.Helper()
	originals, cache := t.TempDir(), t.TempDir()
	mkfile(t, originals, "holiday/photo.jpg")
	return New(testConfig(), originals, cache, proc, 4, 0, "", nil), originals, cache
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestGenerateAndServe(t *testing.T) {
	proc := &stubProcessor{}
	s, _, cache := newTestServer(t, proc)

	rec := get(t, s, "/holiday/photo-400.webp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/webp" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if got := rec.Body.String(); got != "processed 400 webp q80" {
		t.Errorf("body = %q", got)
	}

	// The derivative is on disk at the deterministic path.
	if _, err := os.Stat(filepath.Join(cache, "holiday", "photo-400.webp")); err != nil {
		t.Errorf("derivative not written: %v", err)
	}

	// No leftover temp files.
	entries, err := os.ReadDir(filepath.Join(cache, "holiday"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("cache dir has %d entries, want 1", len(entries))
	}

	// Second request is served from disk without reprocessing.
	rec = get(t, s, "/holiday/photo-400.webp")
	if rec.Code != http.StatusOK {
		t.Fatalf("second request status = %d", rec.Code)
	}
	if calls := proc.calls.Load(); calls != 1 {
		t.Errorf("processor called %d times, want 1", calls)
	}
}

func TestOriginalTokenQuality(t *testing.T) {
	proc := &stubProcessor{}
	s, _, _ := newTestServer(t, proc)
	rec := get(t, s, "/holiday/photo-original.webp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != "processed original webp q90" {
		t.Errorf("body = %q", got)
	}
}

func TestMissingOriginal404(t *testing.T) {
	s, _, _ := newTestServer(t, &stubProcessor{})
	if rec := get(t, s, "/holiday/nope-400.webp"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestBadRequests400(t *testing.T) {
	s, _, _ := newTestServer(t, &stubProcessor{})
	for _, path := range []string{
		"/holiday/photo-500.webp",      // dimension not allowlisted
		"/holiday/photo-400.png",       // unsupported output format
		"/holiday/photo.webp",          // no token
		"/holiday/photo-400x400t.jpeg", // transparent pad on jpeg
	} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, rec.Code)
		}
	}
}

func TestRootRedirect(t *testing.T) {
	s, _, _ := newTestServer(t, &stubProcessor{})
	s.rootRedirect = "https://example.com/"

	rec := get(t, s, "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/" {
		t.Errorf("Location = %q", loc)
	}

	// Only exactly "/" redirects; other unmatched paths keep their errors.
	if rec := get(t, s, "/about"); rec.Code != http.StatusBadRequest {
		t.Errorf("/about status = %d, want 400", rec.Code)
	}
}

func TestRootWithoutRedirect404(t *testing.T) {
	s, _, _ := newTestServer(t, &stubProcessor{})
	if rec := get(t, s, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestProcessingFailure500(t *testing.T) {
	s, _, cache := newTestServer(t, &stubProcessor{fail: true})
	if rec := get(t, s, "/holiday/photo-400.webp"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	// Failure leaves no partial or temp files behind.
	entries, _ := os.ReadDir(filepath.Join(cache, "holiday"))
	if len(entries) != 0 {
		t.Errorf("cache dir has %d entries after failure, want 0", len(entries))
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s, _, _ := newTestServer(t, &stubProcessor{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/holiday/photo-400.webp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestSingleflightDedup(t *testing.T) {
	proc := &stubProcessor{block: make(chan struct{})}
	s, _, _ := newTestServer(t, proc)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := get(t, s, "/holiday/photo-400.webp")
			codes[i] = rec.Code
		}(i)
	}

	// Let all requests pile up on the singleflight, then release.
	for proc.calls.Load() == 0 {
	}
	close(proc.block)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: status = %d", i, code)
		}
	}
	if calls := proc.calls.Load(); calls != 1 {
		t.Errorf("processor called %d times for concurrent identical requests, want 1", calls)
	}
}

func TestGenerationTimeout503(t *testing.T) {
	proc := &stubProcessor{block: make(chan struct{})} // never released
	s, _, _ := newTestServer(t, proc)
	s.generateTimeout = 20 * time.Millisecond

	rec := get(t, s, "/holiday/photo-400.webp")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	close(proc.block) // let the blocked goroutine exit
}

func TestExistingFileServedWithoutProcessor(t *testing.T) {
	proc := &stubProcessor{}
	s, _, cache := newTestServer(t, proc)
	mkNamed(t, cache, "holiday/photo-400.webp", "already cached")

	rec := get(t, s, "/holiday/photo-400.webp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body, _ := io.ReadAll(rec.Body); string(body) != "already cached" {
		t.Errorf("body = %q", body)
	}
	if calls := proc.calls.Load(); calls != 0 {
		t.Errorf("processor called %d times, want 0", calls)
	}
}

func mkNamed(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
