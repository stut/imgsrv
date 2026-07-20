// Command imgsrv is an on-demand image resizer/transcoder that sits behind
// nginx. nginx serves cache hits from disk; misses are proxied here, where
// the derivative is generated once, written atomically to the cache, and
// served.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/stut/imgsrv/internal/config"
	"github.com/stut/imgsrv/internal/processor"
	"github.com/stut/imgsrv/internal/server"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	originalsRoot := envOr("ORIGINALS_ROOT", "/originals")
	cacheRoot := envOr("CACHE_ROOT", "/cache")
	port := envOr("PORT", "8080")
	healthPort := envOr("HEALTH_PORT", "8081")
	configPath := envOr("CONFIG", "/etc/imgsrv/config.yaml")

	generateTimeout, err := durationOr("GENERATE_TIMEOUT", 30*time.Second)
	if err != nil {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	shutdownVips, err := processor.Startup(log)
	if err != nil {
		return fmt.Errorf("libvips startup: %w", err)
	}
	defer shutdownVips()

	srv := server.New(cfg, originalsRoot, cacheRoot, processor.New(), runtime.NumCPU(), generateTimeout, log)

	main := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		// A GET has no body, so ReadTimeout just backstops slow-header
		// clients past ReadHeaderTimeout. WriteTimeout must exceed the
		// generation bound (the handler generates before it writes) plus a
		// margin to stream the result to a slow client; otherwise a legit
		// slow generation would have its connection killed mid-flight.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: generateTimeout + 30*time.Second,
		IdleTimeout:  60 * time.Second,
	}
	health := &http.Server{
		Addr: ":" + healthPort,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 2)
	go func() { errc <- main.ListenAndServe() }()
	go func() { errc <- health.ListenAndServe() }()
	log.Info("imgsrv started", "version", version, "port", port, "health_port", healthPort,
		"originals", originalsRoot, "cache", cacheRoot)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errc:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := main.Shutdown(ctx)
		if herr := health.Shutdown(ctx); err == nil {
			err = herr
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		return err
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s must not be negative", key)
	}
	return d, nil
}
