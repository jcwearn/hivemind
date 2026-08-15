// Command hivemind serves the hivemind party game: one snake, everybody steers.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jcwearn/hivemind/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(log)

	// Reported here rather than where it is discovered: loadConfig runs before
	// there is a configured logger, so anything it logged itself would come out
	// in a different format than every other line this process writes.
	if cfg.ephemeralSecret {
		log.Warn("HIVEMIND_COOKIE_SECRET unset, generated an ephemeral one; players will lose their seats on restart")
	}

	srv := web.New(web.Options{
		Logger:       log,
		BaseURL:      cfg.baseURL,
		CookieSecret: cfg.cookieSecret,
		SecureCookie: strings.HasPrefix(cfg.baseURL, "https://"),
	})

	httpSrv := &http.Server{
		Addr:    cfg.addr,
		Handler: srv.Routes(),
		// No WriteTimeout: an SSE response is an ordinary response as far as
		// net/http is concerned, so any write deadline would sever every live
		// stream on a timer. Read side stays bounded, and idle streams are
		// held open deliberately -- see web.Server.stream.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.addr, "base_url", cfg.baseURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Order matters. srv.Shutdown ends every live SSE stream and stops every
	// room goroutine; httpSrv.Shutdown then has nothing left to wait on.
	//
	// Doing it the other way round would hang for the full timeout on every
	// connected phone: an SSE response is a normal http.ResponseWriter, not a
	// hijacked connection, so httpSrv.Shutdown waits for each one to return on
	// its own -- and a stream that is working correctly never does.
	srv.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info("stopped")
	return nil
}

type config struct {
	addr            string
	baseURL         string
	logLevel        slog.Level
	cookieSecret    []byte
	ephemeralSecret bool
}

func loadConfig() (config, error) {
	cfg := config{
		addr:    envOr("HIVEMIND_ADDR", ":8080"),
		baseURL: strings.TrimSuffix(envOr("HIVEMIND_BASE_URL", "http://localhost:8080"), "/"),
	}

	if err := cfg.logLevel.UnmarshalText([]byte(envOr("HIVEMIND_LOG_LEVEL", "info"))); err != nil {
		return config{}, fmt.Errorf("HIVEMIND_LOG_LEVEL: %w", err)
	}

	// The secret signs the cookie that survives a phone locking its screen.
	// Generating one when unset keeps `docker run` with no arguments working;
	// the cost is that a restart invalidates every seat, which is why anything
	// long-lived should set it. It is never fatal to omit, so this warns rather
	// than failing -- a party that cannot start is worse than a party whose
	// players have to rejoin.
	if s := os.Getenv("HIVEMIND_COOKIE_SECRET"); s != "" {
		cfg.cookieSecret = []byte(s)
	} else {
		cfg.cookieSecret = make([]byte, 32)
		if _, err := rand.Read(cfg.cookieSecret); err != nil {
			return config{}, fmt.Errorf("generate cookie secret: %w", err)
		}
		cfg.ephemeralSecret = true
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
