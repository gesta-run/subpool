package adapter

import (
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
)

type Observer = protocol.Observer

type Response struct {
	*http.Response
	format string
	tools  map[string]protocol.ToolInfo
}

func (response *Response) Render(w http.ResponseWriter, stream bool, request Request, observer Observer) {
	protocol.NewRenderer(observer).Render(w, stream, request.Kind, response.format, request.Model, response.tools, response.Response, request.IncludeUsage)
}

func (response *Response) StreamHTTP(model string, observer Observer) *http.Response {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || response.format != "chat_completions" {
		return response.Response
	}
	return protocol.NewRenderer(observer).BridgeResponses(response.Response, model, response.tools)
}
