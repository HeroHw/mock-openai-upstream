// Command mockupstream is a standalone mock of OpenAI / Anthropic / Gemini /
// Alibaba DashScope upstream provider APIs. Point a gateway channel's BaseURL at
// it to exercise routing, billing, SSE streaming and image/video generation
// (sync and async) without calling real upstreams. See the implementation doc.
//
// It depends only on the Go standard library and starts in well under a second.
//
// SECURITY: this service performs no authentication by default (intentional, to
// keep integration cheap). Bind it to localhost / an internal network only —
// never expose it to the public internet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mock-upstream/internal/mockupstream"
)

func main() {
	configPath := flag.String("config", "", "path to a JSON config file (overrides defaults; env vars override the file). Falls back to $MOCK_CONFIG.")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /__mock/healthz endpoint and exit 0 (healthy) or 1 (unhealthy). For container HEALTHCHECK in scratch images that lack curl.")
	flag.Parse()

	cfg := mockupstream.MustLoadConfig(*configPath)

	// Self-probe mode: the scratch runtime image has no shell or curl, so the
	// binary checks its own health endpoint and reports via exit code.
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	srv := mockupstream.NewServer(cfg)

	httpServer := &http.Server{
		Addr:    mockupstream.ListenAddr,
		Handler: srv.Handler(),
		// No ReadHeaderTimeout/WriteTimeout: sync image/video handlers
		// intentionally hold the connection for ~60s+ (doc §7) and timeouts
		// here would defeat that. The gateway's own timeouts are what we test.
	}

	auth := "no auth — bind to internal network only"
	if cfg.APIKey != "" {
		auth = "Bearer auth enabled (MOCK_API_KEY set)"
	} else if cfg.RequireKey {
		auth = "auth required (any non-empty credential)"
	}
	mockupstream.Logf("listening on %s (%s)", mockupstream.ListenAddr, auth)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			mockupstream.Logf("server error: %v", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	mockupstream.Logf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

// runHealthcheck probes /__mock/healthz on the local listener and returns a
// process exit code: 0 healthy, 1 otherwise.
func runHealthcheck() int {
	host := mockupstream.ListenAddr
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	url := fmt.Sprintf("http://%s/__mock/healthz", host)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
