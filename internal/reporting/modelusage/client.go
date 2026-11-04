// Package modelusage reads Gesta's aggregate employee model feed.
package modelusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Model struct {
	Model string `json:"model"`
	Turns int64  `json:"turns"`
}

type Employee struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Email  string  `json:"email"`
	Models []Model `json:"models"`
}

type Snapshot struct {
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	GeneratedAt time.Time  `json:"generated_at"`
	Employees   []Employee `json:"employees"`
}

type Client struct {
	URL, Token         string
	HTTP               *http.Client
	mu                 sync.Mutex
	cached             Snapshot
	lastStart, lastEnd time.Time
	lastError          error
	retryAt            time.Time
}

// Read caches one immutable reporting window. Daily scheduler ticks and retries
// must not repeatedly fetch identical analytics or mutate a frozen email.
func (c *Client) Read(ctx context.Context, start, end time.Time) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastStart.Equal(start) && c.lastEnd.Equal(end) && (c.lastError == nil || time.Now().Before(c.retryAt)) {
		return c.cached, c.lastError
	}
	r, err := c.fetch(ctx, start, end)
	c.lastStart, c.lastEnd, c.cached, c.lastError = start, end, r, err
	c.retryAt = time.Now().Add(time.Minute)
	return r, err
}

func (c *Client) fetch(ctx context.Context, start, end time.Time) (Snapshot, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return Snapshot{}, fmt.Errorf("invalid Gesta report URL")
	}
	q := u.Query()
	q.Set("start", start.UTC().Format(time.RFC3339Nano))
	q.Set("end", end.UTC().Format(time.RFC3339Nano))
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("invalid Gesta report request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	httpClient := http.Client{}
	if c.HTTP != nil {
		httpClient = *c.HTTP
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("Gesta usage report request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("Gesta usage report returned HTTP %d", response.StatusCode)
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(io.LimitReader(response.Body, (2<<20)+1))
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("invalid Gesta usage report")
	}
	if !snapshot.Start.Equal(start) || !snapshot.End.Equal(end) || snapshot.GeneratedAt.IsZero() || snapshot.GeneratedAt.Before(end.Add(-time.Minute)) || snapshot.GeneratedAt.After(time.Now().Add(time.Minute)) || len(snapshot.Employees) > 200 {
		return Snapshot{}, fmt.Errorf("Gesta usage report window or freshness mismatch")
	}
	if err := validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validate(s Snapshot) error {
	seen := map[string]bool{}
	for _, e := range s.Employees {
		if e.ID == "" || seen[e.ID] || len(e.Models) > 200 {
			return fmt.Errorf("invalid Gesta employee report")
		}
		seen[e.ID] = true
		var total int64
		for _, m := range e.Models {
			if m.Turns < 0 || m.Turns > 1_000_000_000 {
				return fmt.Errorf("invalid Gesta model count")
			}
			total += m.Turns
		}
		if total > 1_000_000_000 {
			return fmt.Errorf("invalid Gesta employee count")
		}
	}
	return nil
}
