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
	"github.com/gesta-run/subpool/internal/jsonobject"
)

func (provider copilotAdapter) compact(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	plan, err := copilotcompact.Prepare(request.Body)
	if err != nil {
		return nil, &RequestError{Code: "unsupported_compaction", Message: err.Error()}
	}
	summary, rejected, err := provider.runCompaction(ctx, request, plan, headers, credentials, options)
	if err != nil || rejected != nil {
		return rejected, err
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

func (provider copilotAdapter) runCompaction(ctx context.Context, request Request, plan copilotcompact.Plan, headers http.Header, credentials Credentials, options Options) (copilotcompact.Summary, *Response, error) {
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
			if options.compactionSampled != nil {
				options.compactionSampled()
			}
		}
		return summary, err
	})
	if err != nil {
		var rejected *copilotcompact.Rejection
		if errors.As(err, &rejected) {
			status := rejected.Response.StatusCode
			if completedCalls > 0 && (status == 401 || status == 429 || status >= 500) {
				_ = rejected.Response.Body.Close()
				return summary, nil, interruptedCompaction()
			}
			return summary, &Response{Response: rejected.Response, format: request.Kind}, nil
		}
		if ctx.Err() != nil {
			return summary, nil, ctx.Err()
		}
		if completedCalls > 0 {
			return summary, nil, interruptedCompaction()
		}
		if errors.Is(err, copilotcompact.ErrUnsupported) {
			return summary, nil, &RequestError{Code: "unsupported_compaction", Message: err.Error()}
		}
		if errors.Is(err, copilotcompact.ErrInvalidSummary) {
			return summary, nil, &ResultError{Code: "invalid_compaction_response", Message: err.Error()}
		}
		return summary, nil, err
	}
	return summary, nil, nil
}

func interruptedCompaction() error {
	return &ResultError{Code: "compaction_interrupted", Message: "Copilot compaction stopped after partial inference. No replacement window was installed; retry compaction explicitly."}
}

func (provider copilotAdapter) compactInband(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	plan, triggerID, err := copilotcompact.PrepareInband(request.Body)
	if err != nil {
		return nil, &RequestError{Code: "unsupported_compaction", Message: copilotcompact.ErrUnsupported.Error()}
	}
	if options.CompactionState == nil || options.CompactionState.Cipher == nil || options.CompactionState.Scope == "" {
		return nil, &ResultError{Code: "compaction_unavailable", Message: "Subpool compaction state encryption is unavailable"}
	}
	if err := copilotcompact.ValidateStateSize(plan, options.compactionCovered); err != nil {
		return nil, &RequestError{Code: "unsupported_compaction", Message: copilotcompact.ErrUnsupported.Error()}
	}
	if stream, _ := jsonobject.Lookup(request.Body, "stream"); string(stream) == "true" {
		return provider.streamCompaction(ctx, request, plan, triggerID, headers, credentials, options)
	}
	summary, rejected, err := provider.runCompaction(ctx, request, plan, headers, credentials, options)
	if err != nil || rejected != nil {
		return rejected, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item, err := options.CompactionState.Seal(copilotcompact.Window(plan, summary.Text), options.compactionCovered, triggerID)
	if err != nil {
		return nil, &ResultError{Code: "compaction_state_failed", Message: "Subpool could not seal compaction state; no replacement window was installed"}
	}
	response := copilotcompact.InbandResponse(request.Model, item, summary)
	return &Response{Response: response, format: "responses", usageRecorded: options.InternalUsage != nil, noSession: true}, nil
}

func (provider copilotAdapter) streamCompaction(ctx context.Context, request Request, plan copilotcompact.Plan, triggerID string, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	job := copilotcompact.InbandJob{
		Model: request.Model, TriggerID: triggerID, Plan: plan, State: options.CompactionState, Covered: options.compactionCovered,
		Failure: compactionStreamFailure,
		Run: func(ctx context.Context, started, sampled func()) (copilotcompact.Summary, error) {
			options.compactionStarted, options.compactionSampled = started, sampled
			summary, rejected, err := provider.runCompaction(ctx, request, plan, headers, credentials, options)
			if rejected != nil {
				return summary, &copilotcompact.Rejection{Response: rejected.Response}
			}
			return summary, err
		},
	}
	response, pending, err := job.Stream(ctx)
	if err != nil {
		if errors.Is(err, copilotcompact.ErrStateSeal) {
			return nil, &ResultError{Code: "compaction_state_failed", Message: copilotcompact.ErrStateSeal.Error()}
		}
		var rejected *copilotcompact.Rejection
		if errors.As(err, &rejected) {
			return &Response{Response: rejected.Response, format: "responses"}, nil
		}
		return nil, err
	}
	return &Response{Response: response, format: "responses", usageRecorded: options.InternalUsage != nil, noSession: true, holdsRequestBody: pending}, nil
}

func compactionStreamFailure(err error) (string, string) {
	var failure *ResultError
	if errors.As(err, &failure) {
		return failure.Code, failure.Message
	}
	if errors.Is(err, copilotcompact.ErrStateSeal) {
		return "compaction_state_failed", copilotcompact.ErrStateSeal.Error()
	}
	return "compaction_interrupted", interruptedCompaction().Error()
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
	if options.compactionStarted != nil {
		options.compactionStarted()
	}
	text, input, output, err := protocol.ReadCopilotSummary(response.Response, response.format)
	if err != nil {
		err = copilotcompact.ErrInvalidSummary
	}
	return copilotcompact.Summary{Text: text, InputTokens: input, OutputTokens: output}, err
}
