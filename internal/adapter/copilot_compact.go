package adapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
)

func (provider copilotAdapter) compact(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	plan, err := copilotcompact.Prepare(request.Body)
	if err != nil {
		return nil, &RequestError{Code: "unsupported_compaction", Message: err.Error()}
	}
	var inputTokens, outputTokens int64
	completedCalls := 0
	defer func() {
		if options.InternalUsage != nil && (inputTokens > 0 || outputTokens > 0) {
			options.InternalUsage(inputTokens, outputTokens)
		}
	}()
	summary, err := copilotcompact.Run(ctx, plan, func(ctx context.Context, body []byte) (copilotcompact.Summary, error) {
		summary, err := provider.summarize(ctx, request.Model, body, headers, credentials, options)
		inputTokens += summary.InputTokens
		outputTokens += summary.OutputTokens
		if err == nil {
			completedCalls++
		}
		return summary, err
	})
	if err != nil {
		var rejected *copilotcompact.Rejection
		if errors.As(err, &rejected) {
			status := rejected.Response.StatusCode
			if completedCalls > 0 && (status == 401 || status == 429 || status >= 500) {
				_ = rejected.Response.Body.Close()
				return nil, interruptedCompaction()
			}
			return &Response{Response: rejected.Response, format: "responses_compact"}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if completedCalls > 0 {
			return nil, interruptedCompaction()
		}
		if errors.Is(err, copilotcompact.ErrUnsupported) {
			return nil, &RequestError{Code: "unsupported_compaction", Message: err.Error()}
		}
		if errors.Is(err, copilotcompact.ErrInvalidSummary) {
			return nil, &ResultError{Code: "invalid_compaction_response", Message: err.Error()}
		}
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"id": "cmp_subpool_" + hex.EncodeToString(id), "object": "response.compaction", "created_at": time.Now().Unix(),
		"output": copilotcompact.Window(plan, summary.Text),
		"usage":  map[string]int64{"input_tokens": summary.InputTokens, "output_tokens": summary.OutputTokens, "total_tokens": summary.InputTokens + summary.OutputTokens},
	})
	if err != nil {
		return nil, err
	}
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("X-Subpool-Compaction", "text-summary")
	return &Response{Response: response, format: "responses_compact", usageRecorded: options.InternalUsage != nil}, nil
}

func interruptedCompaction() error {
	return &ResultError{Code: "compaction_interrupted", Message: "Copilot compaction stopped after partial inference. No replacement window was installed; retry compaction explicitly."}
}

func (provider copilotAdapter) summarize(ctx context.Context, model string, body []byte, headers http.Header, credentials Credentials, options Options) (copilotcompact.Summary, error) {
	response, err := provider.RoundTrip(ctx, Request{Kind: "responses", Model: model, Body: body}, headers, credentials, options)
	if err != nil {
		return copilotcompact.Summary{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		capacity := response.StatusCode == http.StatusRequestEntityTooLarge
		if response.StatusCode == http.StatusBadRequest {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			if readErr != nil {
				_ = response.Body.Close()
				return copilotcompact.Summary{}, readErr
			}
			response.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(raw), response.Body), Closer: response.Body}
			var envelope struct {
				Error struct{ Code string } `json:"error"`
			}
			// Only the safely normalized machine code makes a 400 retryable.
			capacity = json.Unmarshal(raw, &envelope) == nil && envelope.Error.Code == "context_length_exceeded"
		}
		return copilotcompact.Summary{}, &copilotcompact.Rejection{Response: response.Response, Capacity: capacity}
	}
	defer response.Body.Close()
	text, input, output, err := protocol.ReadCopilotSummary(response.Response, response.format)
	if err != nil {
		err = copilotcompact.ErrInvalidSummary
	}
	return copilotcompact.Summary{Text: text, InputTokens: input, OutputTokens: output}, err
}
