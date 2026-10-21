package adapter

import (
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
)

type Observer = protocol.Observer

type Response struct {
	*http.Response
	format           string
	tools            map[string]protocol.ToolInfo
	usageRecorded    bool
	noSession        bool
	holdsRequestBody bool
	modelScopedLimit bool
}

// ModelScopedLimit distinguishes adapter-classified model capacity from account limits.
func (response *Response) ModelScopedLimit() bool {
	return response != nil && response.modelScopedLimit
}

// RetainsRequestBody reports work that outlives upstream response headers.
// Its admission leases must remain held until the response body is closed.
func (response *Response) RetainsRequestBody() bool {
	return response != nil && response.holdsRequestBody
}

func (response *Response) Render(w http.ResponseWriter, stream bool, request Request, observer Observer) {
	if response.usageRecorded {
		observer.Usage = nil
	}
	if response.noSession {
		observer.Session = nil
	}
	protocol.NewRenderer(observer).Render(w, stream, request.Kind, response.format, request.Model, response.tools, response.Response, request.IncludeUsage)
}

func (response *Response) StreamHTTP(model string, observer Observer) *http.Response {
	// The WebSocket bridge must preserve client-visible usage while avoiding
	// duplicate accounting or bindings for synthetic compaction responses.
	response.Header.Del("X-Subpool-Internal-Usage-Recorded")
	response.Header.Del("X-Subpool-Internal-No-Session")
	if response.usageRecorded {
		response.Header.Set("X-Subpool-Internal-Usage-Recorded", "true")
	}
	if response.noSession {
		response.Header.Set("X-Subpool-Internal-No-Session", "true")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || response.format != "chat_completions" {
		return response.Response
	}
	return protocol.NewRenderer(observer).BridgeResponses(response.Response, model, response.tools)
}
