package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/adapter/antigravitywire"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type AntigravityClient interface {
	Generate(context.Context, string, json.RawMessage, antigravity.Credentials) (*http.Response, error)
}

type antigravityAdapter struct {
	client     AntigravityClient
	signatures *antigravitywire.Signatures
	limits     *antigravitywire.ModelLimits
}

func (p antigravityAdapter) RoundTrip(ctx context.Context, request Request, _ http.Header, credentials Credentials, options Options) (*Response, error) {
	if p.client == nil {
		return nil, errors.New("Antigravity provider is unavailable")
	}
	if request.Kind != "responses" && request.Kind != "chat_completions" {
		return nil, ErrUnsupported
	}
	if delay := p.limits.Remaining(options.AccountID, request.Model); delay > 0 {
		return &Response{Response: antigravityRateLimit(delay), format: "chat_completions", modelScopedLimit: true}, nil
	}
	var meta struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(request.Body, &meta); err != nil {
		return nil, antigravityRequestError(err)
	}
	body := request.Body
	var tools map[string]protocol.ToolInfo
	var err error
	if request.Kind == "responses" {
		body, tools, err = protocol.ResponsesToChat(body, false)
		if err != nil {
			return nil, antigravityRequestError(err)
		}
	}
	body, err = antigravitywire.Request(body, options.AccountID, request.Model, p.signatures)
	if err != nil {
		return nil, antigravityRequestError(err)
	}
	upstream, err := p.client.Generate(ctx, request.Model, body, credentials.Antigravity)
	if err != nil {
		return nil, err
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		modelLimit := normalizeAntigravityError(upstream) || upstream.StatusCode == http.StatusTooManyRequests
		if modelLimit {
			delay := antigravityRetryDelay(upstream.Header)
			p.limits.Set(options.AccountID, request.Model, delay)
		}
		return &Response{Response: upstream, format: "chat_completions", modelScopedLimit: modelLimit}, nil
	}
	response, err := antigravitywire.Response(upstream, meta.Stream, options.AccountID, request.Model, p.signatures)
	if err != nil {
		return nil, err
	}
	return &Response{Response: response, format: "chat_completions", tools: tools, holdsRequestBody: meta.Stream}, nil
}

func antigravityRetryDelay(header http.Header) time.Duration {
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(header.Get("Retry-After")); err == nil && time.Until(until) > 0 {
		return time.Until(until)
	}
	return time.Minute
}

func antigravityRateLimit(delay time.Duration) *http.Response {
	header := make(http.Header)
	header.Set("Retry-After", strconv.FormatInt(max(1, int64(delay.Seconds()+0.999)), 10))
	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: header, Body: io.NopCloser(bytes.NewReader(nil))}
}

func normalizeAntigravityError(response *http.Response) bool {
	defer response.Body.Close()
	var payload struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Message string          `json:"message"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
				Reason     string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
	modelCapacity := false
	var reasons []string
	for _, detail := range payload.Error.Details {
		reasons = append(reasons, detail.Reason)
		if response.StatusCode == http.StatusServiceUnavailable && detail.Reason == "MODEL_CAPACITY_EXHAUSTED" {
			modelCapacity = true
		}
		if delay, err := time.ParseDuration(detail.RetryDelay); err == nil && delay > 0 && response.Header.Get("Retry-After") == "" {
			response.Header.Set("Retry-After", strconv.FormatInt(max(1, int64(delay.Seconds()+0.999)), 10))
		}
	}
	message := "Google Antigravity rejected the request. Check account eligibility, model availability and request capabilities."
	code := "antigravity_request_failed"
	if antigravityModelUnavailable(response.StatusCode, payload.Error.Code, payload.Error.Type, payload.Error.Message, reasons) {
		code, message = "model_not_found", "The requested model is not available for this Google Antigravity account."
	}
	if response.StatusCode == http.StatusRequestEntityTooLarge {
		message = "Google Antigravity rejected an oversized payload; reduce conversation, image and tool output sizes."
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "provider_error", "message": message, "code": code}})
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.Header.Set("Content-Type", "application/json")
	response.Header.Del("Content-Length")
	response.ContentLength = int64(len(body))
	return modelCapacity
}

func antigravityModelUnavailable(status int, code json.RawMessage, kind, message string, reasons []string) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	var namedCode string
	_ = json.Unmarshal(code, &namedCode)
	for _, signal := range append(reasons, namedCode, kind) {
		switch strings.ToLower(strings.ReplaceAll(signal, "-", "_")) {
		case "model_not_found", "unsupported_model", "model_not_supported", "model_unavailable":
			return true
		}
	}
	message = strings.ToLower(message)
	for _, phrase := range []string{"model is not available", "model not found", "unsupported model", "model is not supported", "model does not support this endpoint"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return strings.Contains(message, "model") && strings.Contains(message, "not available for this endpoint")
}

func antigravityRequestError(err error) error {
	converted := conversionError(err).(*RequestError)
	converted.Message = strings.ReplaceAll(converted.Message, "GitHub Copilot", "Google Antigravity")
	return converted
}
