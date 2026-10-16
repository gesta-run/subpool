package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/jsonobject"
)

// Restore Subpool state before provider selection reaches any wire encoder.
// This also prevents leaking our opaque tokens during mixed-pool failover.
type compactionAdapter struct {
	Provider
	copilot bool
}

func (provider compactionAdapter) RoundTrip(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	if request.Kind != "responses" && request.Kind != "responses_compact" {
		return provider.Provider.RoundTrip(ctx, request, headers, credentials, options)
	}
	previous, _ := jsonobject.Lookup(request.Body, "previous_response_id")
	var previousID string
	_ = json.Unmarshal(previous, &previousID)
	if strings.HasPrefix(previousID, "resp_subpool_cmp_") {
		return nil, &RequestError{Code: "previous_response_not_found", Message: "Subpool compaction responses do not store upstream history; replay the compaction item and full remaining input"}
	}
	trigger, _, own := copilotcompact.Features(request.Body)
	if own && previousID != "" {
		return nil, &RequestError{Code: "previous_response_not_found", Message: "Subpool compaction state requires full input replay without previous_response_id"}
	}
	if trigger && provider.copilot {
		covered, err := copilotcompact.Coverage(request.Body)
		if err != nil {
			return nil, &RequestError{Code: "unsupported_compaction", Message: copilotcompact.ErrUnsupported.Error()}
		}
		options.compactionCovered = covered
	}
	if own {
		body, err := options.CompactionState.Restore(request.Body)
		if err != nil {
			return nil, &RequestError{Code: "invalid_compaction_state", Message: copilotcompact.ErrInvalidState.Error()}
		}
		request.Body = body
	}
	return provider.Provider.RoundTrip(ctx, request, headers, credentials, options)
}
