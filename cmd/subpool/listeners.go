package main

import (
	"context"
	"fmt"
	"github.com/gesta-run/subpool/internal/config"
	"github.com/gesta-run/subpool/internal/control"
	"github.com/gesta-run/subpool/internal/gateway"
	"github.com/gesta-run/subpool/internal/store"
	"net/http"
	"time"
)

func httpBindings(cfg config.Config, database *store.Postgres, gatewayServer *gateway.Server, controlServer *control.Server) []serverBinding {
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
		_, _ = w.Write([]byte("# HELP subpool_up Whether the service is running.\n# TYPE subpool_up gauge\nsubpool_up 1\n" + gatewayServer.ResponsesWebSocketMetrics() + fmt.Sprintf("# HELP subpool_codex_quota_routing_switches_total Committed quota-aware default binding changes.\n# TYPE subpool_codex_quota_routing_switches_total counter\nsubpool_codex_quota_routing_switches_total %d\n", database.QuotaRoutingSwitches())))
	})

	consoleMux := http.NewServeMux()
	controlServer.Register(consoleMux)
	registerWeb(consoleMux)

	return []serverBinding{
		{name: "API", server: newHTTPServer(cfg.APIListenAddress, apiMux, cfg.RequestBodyReadTimeout)},
		{name: "console", server: newHTTPServer(cfg.ConsoleListenAddress, consoleOnlyHandler(consoleMux), cfg.RequestBodyReadTimeout)},
	}
}
