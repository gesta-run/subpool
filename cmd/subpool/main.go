package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/config"
	"github.com/gesta-run/subpool/internal/control"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/gateway"
	providerhealth "github.com/gesta-run/subpool/internal/health"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
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
	providerHTTPClient := providerhttp.NewWithResponseHeaderTimeout(cfg.UpstreamResponseHeaderTimeout)
	copilotClient := copilot.NewClient(copilot.ClientConfig{
		APIBase: cfg.CopilotAPIBase, TokenExchangeURL: cfg.CopilotTokenExchangeURL, EntitlementsURL: cfg.CopilotEntitlementsURL, HTTPClient: providerHTTPClient,
	})
	copilotDeviceAuth := copilot.NewDeviceAuth(copilot.DeviceAuthConfig{
		ClientID: cfg.CopilotClientID, TokenExchangeURL: cfg.CopilotTokenExchangeURL, HTTPClient: providerHTTPClient,
	})
	defer copilotDeviceAuth.Close()
	provider := codex.NewClient(cfg.CodexUpstreamURL, providerHTTPClient)
	resetCredits := codex.NewAppServer()
	compatibleProvider := openaicompat.NewClient(providerHTTPClient)
	refreshManager := credential.NewRefreshManager(database, cipher, tokenRefresher)
	healthChecker := providerhealth.NewChecker(database, cipher, resetCredits, compatibleProvider, copilotClient)
	sources, err := auth.NewSourceResolver(cfg.TrustedProxyCIDRs)
	if err != nil {
		slog.Error("trusted proxy configuration failed", "error", err)
		os.Exit(1)
	}
	gatewayServer := gateway.New(database, keys, cipher, provider, refreshManager, compatibleProvider).
		WithCopilot(copilotClient).
		WithRequestBodyLimits(cfg.MaxRequestBodyBytes, cfg.MaxInflightRequestBodyBytes, cfg.RequestBodyReadTimeout).
		WithModelProviders(resetCredits, compatibleProvider, copilotClient).
		WithResponsesWebSocket(cfg.ResponsesWSEnabled, cfg.ResponsesWSForceHTTPBridge, cfg.CodexUpstreamURL)
	controlServer := control.New(database, sessions, keys, cipher, deviceAuth, refreshManager, sources, healthChecker).
		WithCopilotDeviceAuth(copilotDeviceAuth).
		WithResetCredits(resetCredits).
		WithModelProviders(resetCredits, compatibleProvider, copilotClient).
		WithAccountRoutingChange(gatewayServer.CloseResponsesWebSocketsForAccount)

	apiMux := http.NewServeMux()
	gatewayServer.Register(apiMux)
	apiMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if database.Ping(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	apiMux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP subpool_up Whether the service is running.\n# TYPE subpool_up gauge\nsubpool_up 1\n" + gatewayServer.ResponsesWebSocketMetrics()))
	})

	consoleMux := http.NewServeMux()
	controlServer.Register(consoleMux)
	registerWeb(consoleMux)

	bindings := []serverBinding{
		{name: "API", server: newHTTPServer(cfg.APIListenAddress, apiMux, cfg.RequestBodyReadTimeout)},
		{name: "console", server: newHTTPServer(cfg.ConsoleListenAddress, consoleOnlyHandler(consoleMux), cfg.RequestBodyReadTimeout)},
	}
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go healthChecker.Run(stopCtx)
	go database.RunMaintenance(stopCtx)
	if err = serveHTTPServers(stopCtx, bindings, gatewayServer.CloseResponsesWebSockets); err != nil {
		slog.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

type serverBinding struct {
	name   string
	server *http.Server
}

type serverResult struct {
	name string
	err  error
}

func newHTTPServer(address string, handler http.Handler, readTimeout time.Duration) *http.Server {
	return &http.Server{Addr: address, Handler: securityHeaders(handler), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: readTimeout, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
}

func serveHTTPServers(ctx context.Context, bindings []serverBinding, beforeShutdown func()) error {
	results := make(chan serverResult, len(bindings))
	for _, binding := range bindings {
		binding := binding
		slog.Info("Subpool listener is starting", "listener", binding.name, "address", binding.server.Addr)
		go func() {
			results <- serverResult{name: binding.name, err: binding.server.ListenAndServe()}
		}()
	}

	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			if beforeShutdown != nil {
				beforeShutdown()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var pending sync.WaitGroup
			for _, binding := range bindings {
				pending.Add(1)
				go func() {
					defer pending.Done()
					if err := binding.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
						slog.Warn("HTTP server shutdown failed", "listener", binding.name, "error", err)
					}
				}()
			}
			pending.Wait()
		})
	}
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	go func() {
		<-watchCtx.Done()
		shutdown()
	}()

	var firstErr error
	for range bindings {
		result := <-results
		if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) && firstErr == nil {
			firstErr = fmt.Errorf("%s listener: %w", result.name, result.err)
			shutdown()
		}
	}
	shutdown()
	return firstErr
}

func consoleOnlyHandler(console http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/v1" || strings.HasPrefix(path, "/v1/") || path == "/healthz" || path == "/readyz" || path == "/metrics" {
			http.NotFound(w, r)
			return
		}
		console.ServeHTTP(w, r)
	})
}

func registerWeb(mux *http.ServeMux) {
	dist := os.Getenv("SUBPOOL_WEB_DIR")
	if dist == "" {
		dist = "web/dist"
	}
	if info, err := os.Stat(dist); err == nil && info.IsDir() {
		files := http.FileServer(http.Dir(dist))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			cleanPath := pathpkg.Clean("/" + r.URL.Path)
			filePath := filepath.Join(dist, filepath.FromSlash(strings.TrimPrefix(cleanPath, "/")))
			if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
				if filepath.Ext(filePath) == ".html" {
					w.Header().Set("Cache-Control", "no-cache")
				} else if strings.HasPrefix(cleanPath, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					w.Header().Set("Vary", "Accept-Encoding")
					if acceptsGzip(r.Header.Get("Accept-Encoding")) && serveGzipFile(w, r, filePath, info.ModTime()) {
						return
					}
				}
				files.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
		})
	}
}

func acceptsGzip(value string) bool {
	for _, item := range strings.Split(value, ",") {
		parts := strings.Split(item, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			name, raw, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(name, "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				return false
			}
			quality = parsed
		}
		return quality > 0
	}
	return false
}

func serveGzipFile(w http.ResponseWriter, r *http.Request, originalPath string, modTime time.Time) bool {
	file, err := os.Open(originalPath + ".gz")
	if err != nil {
		return false
	}
	defer file.Close()
	w.Header().Set("Content-Encoding", "gzip")
	if contentType := mime.TypeByExtension(filepath.Ext(originalPath)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, filepath.Base(originalPath), modTime, file)
	return true
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
