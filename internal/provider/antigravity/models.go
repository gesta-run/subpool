package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Model struct {
	DisplayName string `json:"displayName"`
	Quota       *struct {
		RemainingFraction float64   `json:"remainingFraction"`
		ResetTime         time.Time `json:"resetTime"`
	} `json:"quotaInfo"`
}
type Snapshot struct {
	Models map[string]Model `json:"models"`
}

func (c *Client) Models(ctx context.Context, credentials Credentials) (Snapshot, error) {
	response, err := c.do(ctx, "fetchAvailableModels", credentials.AccessToken, map[string]string{"project": credentials.ProjectID})
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	err = decodeResponse(response, &snapshot)
	if err == nil && len(snapshot.Models) == 0 {
		err = errors.New("Antigravity returned no available models")
	}
	return snapshot, err
}

func (c *Client) project(ctx context.Context, token string) (string, error) {
	metadata := map[string]string{"ideType": "ANTIGRAVITY", "ideName": "antigravity"}
	response, err := c.do(ctx, "loadCodeAssist", token, map[string]any{"metadata": metadata})
	if err != nil {
		return "", err
	}
	var loaded struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
		Allowed []struct {
			ID      string `json:"id"`
			Default bool   `json:"isDefault"`
		} `json:"allowedTiers"`
	}
	if err = decodeResponse(response, &loaded); err != nil {
		return "", err
	}
	if project := projectID(loaded.Project); project != "" {
		return project, nil
	}
	tier := ""
	for _, candidate := range loaded.Allowed {
		if candidate.Default {
			tier = candidate.ID
			break
		}
	}
	if tier == "" {
		return "", errors.New("Google did not offer an eligible Antigravity tier")
	}
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		response, err = c.do(ctx, "onboardUser", token, map[string]any{"tierId": tier, "metadata": metadata})
		if err != nil {
			return "", err
		}
		var onboard struct {
			Done     bool `json:"done"`
			Response struct {
				Project json.RawMessage `json:"cloudaicompanionProject"`
			} `json:"response"`
		}
		if err = decodeResponse(response, &onboard); err != nil {
			return "", err
		}
		if onboard.Done {
			if project := projectID(onboard.Response.Project); project != "" {
				return project, nil
			}
		}
	}
	return "", errors.New("Antigravity project initialization did not finish; authorize again later")
}

func projectID(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var object struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &object)
	return object.ID
}
