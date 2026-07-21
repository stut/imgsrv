// Package server implements the imgsrv HTTP handler: parse the derivative
// URL, serve from cache if present, otherwise generate exactly once
// (singleflight), write atomically, and serve.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stut/imgsrv/internal/config"
	"github.com/stut/imgsrv/internal/token"
)

// Processor generates a derivative from the original at srcPath and writes
// the encoded image to dstPath (a temporary file; the server renames it).
type Processor interface {
	Process(ctx context.Context, srcPath, dstPath string, tok token.Token, format Format, quality int) error
}

// Server is the imgsrv HTTP handler.
type Server struct {
	cfg             config.Config
	originalsRoot   string
	cacheRoot       string
	processor       Processor
	group           singleflight.Group
	sem             chan struct{} // caps concurrent generations
	generateTimeout time.Duration // wall-clock bound per generation; 0 = none
	rootRedirect    string        // fallback URL for root requests with no per-host file; "" = 404
	log             *slog.Logger
}

// New creates a Server. maxConcurrent caps simultaneous libvips jobs;
// values < 1 mean 1. generateTimeout bounds the wall-clock time a single
// generation may take (including the wait for a free libvips slot); 0
// disables the bound. rootRedirect, when non-empty, is the fallback URL that
// root requests are redirected to when the host has no .root-redirect file;
// empty means such requests return 404.
func New(cfg config.Config, originalsRoot, cacheRoot string, proc Processor, maxConcurrent int, generateTimeout time.Duration, rootRedirect string, log *slog.Logger) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:             cfg,
		originalsRoot:   originalsRoot,
		cacheRoot:       cacheRoot,
		processor:       proc,
		sem:             make(chan struct{}, maxConcurrent),
		generateTimeout: generateTimeout,
		rootRedirect:    rootRedirect,
		log:             log,
	}
}

const cacheControl = "public, max-age=31536000, immutable"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.log.Info("request", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Root requests: bare "/" (running without nginx), or "/<host>/" as
	// proxied by nginx, which prefixes the mapped hostname onto "/" the
	// same way it does for derivatives. Derivative URLs never end in "/",
	// so there's no ambiguity.
	if host, ok := rootRequest(r.URL.Path); ok {
		s.redirectRoot(w, r, host)
		return
	}

	req, err := ParseRequest(r.URL.Path, s.cfg.Dimensions)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	cachePath := req.CachePath(s.cacheRoot)

	// Fast path: derivative already on disk (covers races with nginx).
	if fileExists(cachePath) {
		s.serveFile(w, r, req, cachePath)
		return
	}

	ctx := r.Context()
	if s.generateTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.generateTimeout)
		defer cancel()
	}

	// Exactly one generation per derivative path within this process.
	_, err, _ = s.group.Do(cachePath, func() (any, error) {
		if fileExists(cachePath) {
			return nil, nil // another request just finished it
		}
		return nil, s.generate(ctx, req, cachePath)
	})
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			http.NotFound(w, r)
		case errors.Is(err, context.DeadlineExceeded):
			// Generation ran past its wall-clock bound. Shed load rather
			// than hold the slot; the client (or nginx) may retry.
			s.log.Warn("generation timed out", "path", req.URLPath, "timeout", s.generateTimeout)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		case errors.Is(err, context.Canceled):
			// Client disconnected before generation finished; nothing to serve.
			s.log.Info("request cancelled", "path", req.URLPath)
		default:
			s.log.Error("generation failed", "path", req.URLPath, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}

	s.serveFile(w, r, req, cachePath)
}

// rootRedirectFile, dropped into a host's originals directory, holds the URL
// that requests for that host's "/" redirect to. Read per request, so
// adding or changing it needs no restart.
const rootRedirectFile = ".root-redirect"

// rootRequest reports whether path is a root request and, when nginx has
// prefixed the mapped hostname ("/imgsrv.net/"), which host directory it
// is for. A bare "/" is a root request with no host.
func rootRequest(path string) (host string, ok bool) {
	if path == "/" {
		return "", true
	}
	if len(path) > 2 && path[0] == '/' && path[len(path)-1] == '/' {
		if seg := path[1 : len(path)-1]; !strings.Contains(seg, "/") {
			return seg, true
		}
	}
	return "", false
}

// redirectRoot answers a root request: the host's .root-redirect file wins,
// the ROOT_REDIRECT fallback is next, otherwise 404.
func (s *Server) redirectRoot(w http.ResponseWriter, r *http.Request, host string) {
	// nginx's host map can't produce "." or "..", but the bare binary has
	// no such guard; never let the segment escape the originals root.
	if host == "." || host == ".." {
		http.NotFound(w, r)
		return
	}
	if host != "" {
		b, err := os.ReadFile(filepath.Join(s.originalsRoot, host, rootRedirectFile))
		switch {
		case err == nil:
			if url := strings.TrimSpace(string(b)); url != "" {
				http.Redirect(w, r, url, http.StatusFound)
				return
			}
			s.log.Warn("empty root-redirect file", "host", host)
		case !errors.Is(err, os.ErrNotExist):
			s.log.Warn("reading root-redirect file", "host", host, "error", err)
		}
	}
	if s.rootRedirect != "" {
		http.Redirect(w, r, s.rootRedirect, http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) generate(ctx context.Context, req Request, cachePath string) error {
	src, err := ResolveOriginal(s.originalsRoot, req, s.cfg.InputExtensions)
	if err != nil {
		return err
	}

	// Cap concurrent libvips jobs; excess requests queue here.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	quality := s.cfg.Quality.Default
	if req.Token.Original {
		quality = s.cfg.Quality.Original
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}

	// Never write directly to the final filename. A random temp name per
	// write means two instances sharing the cache mount (rolling updates)
	// can't clobber each other's in-progress file.
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), filepath.Base(cachePath)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after a successful rename

	if err := s.processor.Process(ctx, src, tmpPath, req.Token, req.Format, quality); err != nil {
		return fmt.Errorf("processing %s: %w", req.URLPath, err)
	}
	// CreateTemp made the file 0600; nginx's non-root workers serve the
	// cache directly, so open it up before publishing.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		return err
	}
	s.log.Info("generated", "path", req.URLPath, "original", src)
	return nil
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, req Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		s.log.Error("serving cached file", "path", path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.log.Error("serving cached file", "path", path, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", req.Format.ContentType())
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
