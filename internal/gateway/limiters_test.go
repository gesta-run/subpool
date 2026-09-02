package gateway

import (
	"testing"
	"time"
)

func TestModelCacheEvictsExpiredPools(t *testing.T) {
	cache := newModelCache()
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cache.Set("expired", []exposedModel{{ID: "gpt-test"}}, start.Add(modelCacheTTL))
	cache.Set("active", []exposedModel{{ID: "gpt-new"}}, start.Add(3*modelCacheTTL))

	models, found := cache.Get("active", start.Add(2*modelCacheTTL))
	if !found || len(models) != 1 || models[0].ID != "gpt-new" {
		t.Fatalf("active cache entry = %#v, found=%v", models, found)
	}
	if _, exists := cache.entries["expired"]; exists {
		t.Fatal("expired pool was not evicted")
	}
}
