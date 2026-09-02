package store

import (
	"context"
	"log/slog"
	"time"
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
		tag, err := p.pool.Exec(ctx, `WITH expired AS (
			SELECT ctid FROM usage_event_dedup WHERE created_at < $1 ORDER BY created_at LIMIT $2
		) DELETE FROM usage_event_dedup target USING expired WHERE target.ctid=expired.ctid`, cutoff, maintenanceBatch)
		if err != nil {
			slog.Error("usage dedup maintenance failed", "error", err)
			return
		}
		if tag.RowsAffected() < maintenanceBatch {
			break
		}
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM session_bindings WHERE expires_at < now()`); err != nil {
		slog.Error("session binding maintenance failed", "error", err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at < now()`); err != nil {
		slog.Error("admin session maintenance failed", "error", err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM admin_login_failures WHERE reset_at < now()`); err != nil {
		slog.Error("admin login failure maintenance failed", "error", err)
	}
}
