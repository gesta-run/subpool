package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/config"
	"github.com/gesta-run/subpool/internal/control"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/gateway"
	providerhealth "github.com/gesta-run/subpool/internal/health"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	database, err := store.Open(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		slog.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	cipher, err := credential.New(cfg.CredentialKey)
	if err != nil {
		slog.Error("credential cipher startup failed", "error", err)
		os.Exit(1)
	}
	keys := auth.NewAPIKeys(cfg.APIKeyHMACKey)
	publicURL, _ := url.Parse(cfg.PublicURL)
	sessions := auth.NewAdminSessions(cfg.AdminUsername, cfg.AdminPassword, cfg.SessionTTL, publicURL.Scheme == "https", cfg.APIKeyHMACKey, database)
	tokenRefresher := codex.NewTokenRefresher(codex.TokenRefresherConfig{ClientID: cfg.CodexClientID, TokenURL: cfg.CodexTokenURL})
	deviceAuth := codex.NewDeviceAuth()
	defer deviceAuth.Close()
	provider := codex.NewClient(cfg.CodexUpstreamURL, nil)
	resetCredits := codex.NewAppServer()
	compatibleProvider := openaicompat.NewClient(nil)
	refreshManager := credential.NewRefreshManager(database, cipher, tokenRefresher)
	healthChecker := providerhealth.NewChecker(database, cipher, provider, compatibleProvider)
	sources, err := auth.NewSourceResolver(cfg.TrustedProxyCIDRs)
	if err != nil {
		slog.Error("trusted proxy configuration failed", "error", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	control.New(database, sessions, keys, cipher, deviceAuth, refreshManager, sources, healthChecker).
		WithResetCredits(resetCredits).
		WithModelProviders(resetCredits, compatibleProvider).
		Register(mux)
	gatewayServer := gateway.New(database, keys, cipher, provider, refreshManager, compatibleProvider).
		WithModelProviders(resetCredits, compatibleProvider).
		WithResponsesWebSocket(cfg.ResponsesWSEnabled, cfg.ResponsesWSForceHTTPBridge, cfg.CodexUpstreamURL)
	gatewayServer.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if database.Ping(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP subpool_up Whether the service is running.\n# TYPE subpool_up gauge\nsubpool_up 1\n" + gatewayServer.ResponsesWebSocketMetrics()))
	})
	registerWeb(mux)
	server := &http.Server{Addr: cfg.ListenAddress, Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go healthChecker.Run(stopCtx)
	go database.RunMaintenance(stopCtx)
	go func() {
		<-stopCtx.Done()
		gatewayServer.CloseResponsesWebSockets()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("Subpool is listening", "address", cfg.ListenAddress)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

func registerWeb(mux *http.ServeMux) {
	dist := os.Getenv("SUBPOOL_WEB_DIR")
	if dist == "" {
		dist = "web/dist"
	}
	if info, err := os.Stat(dist); err == nil && info.IsDir() {
		files := http.FileServer(http.Dir(dist))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			path := filepath.Join(dist, filepath.Clean(r.URL.Path))
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
		})
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
