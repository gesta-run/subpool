package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/gesta-run/subpool/internal/store/storedb"
)

const (
	usageDedupRetention = 8 * 24 * time.Hour
	maintenanceInterval = 24 * time.Hour
	maintenanceBatch    = 10_000
	maintenanceBatches  = 10
)

func (p *Postgres) RunMaintenance(ctx context.Context) {
	p.runMaintenance(ctx)
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.runMaintenance(ctx)
		}
	}
}

func (p *Postgres) runMaintenance(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-usageDedupRetention)
	for batch := 0; batch < maintenanceBatches; batch++ {
		rows, err := p.queries.DeleteExpiredUsageDedup(ctx, storedb.DeleteExpiredUsageDedupParams{
			Cutoff: dbTime(cutoff), BatchLimit: maintenanceBatch,
		})
		if err != nil {
			slog.Error("usage dedup maintenance failed", "error", err)
			return
		}
		if rows < maintenanceBatch {
			break
		}
	}
	if err := p.queries.DeleteExpiredSessionBindings(ctx); err != nil {
		slog.Error("session binding maintenance failed", "error", err)
	}
	if err := p.queries.DeleteExpiredAdminSessions(ctx); err != nil {
		slog.Error("admin session maintenance failed", "error", err)
	}
	if err := p.queries.DeleteExpiredAdminLoginFailures(ctx); err != nil {
		slog.Error("admin login failure maintenance failed", "error", err)
	}
}
