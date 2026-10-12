package protocol

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gesta-run/subpool/internal/protocol/responseevent"
)

type Observer struct {
	Now          func() time.Time
	NextSequence func() uint64
	Usage        func(string, string, int64, int64)
	Session      func(string)
	Limit        func(responseevent.LimitKind)
}

type Renderer struct {
	hooks        Observer
	includeUsage bool
}

func NewRenderer(observer Observer) *Renderer {
	if observer.Now == nil {
		observer.Now = time.Now
	}
	if observer.NextSequence == nil {
		sequence := new(atomic.Uint64)
		observer.NextSequence = func() uint64 { return sequence.Add(1) }
	}
	if observer.Usage == nil {
		observer.Usage = func(string, string, int64, int64) {}
	}
	if observer.Session == nil {
		observer.Session = func(string) {}
	}
	if observer.Limit == nil {
		observer.Limit = func(responseevent.LimitKind) {}
	}
	return &Renderer{hooks: observer}
}

func (renderer *Renderer) Render(w http.ResponseWriter, stream bool, kind, format, model string, tools map[string]ToolInfo, response *http.Response, includeUsage ...bool) {
	renderer.includeUsage = len(includeUsage) > 0 && includeUsage[0]
	if kind == "responses_compact" {
		renderer.proxyCompactJSON(w, model, response)
		return
	}
	if kind == "chat_completions" {
		renderer.renderChat(w, stream, format, model, response)
		return
	}
	if format == "chat_completions" {
		if stream {
			renderer.proxyCopilotResponsesStream(w, model, tools, response)
		} else {
			renderer.proxyCopilotResponsesJSON(w, model, tools, response)
		}
		return
	}
	if stream {
		renderer.proxyResponsesStream(w, model, response)
	} else {
		renderer.proxyResponsesJSON(w, model, response)
	}
}

func (renderer *Renderer) renderChat(w http.ResponseWriter, stream bool, format, model string, response *http.Response) {
	if format == "chat_completions" {
		if stream {
			renderer.proxyCompatibleChatStream(w, model, response)
		} else {
			renderer.proxyCompatibleChatJSON(w, model, response)
		}
		return
	}
	if stream {
		renderer.proxyChatStream(w, model, response)
	} else {
		renderer.proxyChatJSON(w, model, response)
	}
}
