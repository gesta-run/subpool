package copilotpayload

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const maxErrorBytes = 64 << 10

type Reason string

const (
	Unknown Reason = "unknown"
	Context Reason = "context_tokens"
	Body    Reason = "request_bytes"
	Images  Reason = "images"
)

type replayBody struct {
	io.Reader
	io.Closer
}

// Inspect accepts only explicit machine-readable codes. Error messages may echo
// prompts, and HTTP 413 alone does not identify a model's context-token limit.
// Unrecognized responses remain readable, including bodies above our read cap.
func Inspect(response *http.Response) Reason {
	if response == nil || response.Body == nil ||
		(response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusRequestEntityTooLarge) {
		return Unknown
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "application/json") ||
		response.Header.Get("Content-Encoding") != "" {
		return Unknown
	}
	original := response.Body
	raw, err := io.ReadAll(io.LimitReader(original, maxErrorBytes+1))
	response.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(raw), original), Closer: original}
	if err != nil || len(raw) > maxErrorBytes {
		return Unknown
	}
	var envelope struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return Unknown
	}
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Code
	}
	switch code {
	case "context_length_exceeded", "context_window_exceeded", "max_prompt_tokens_exceeded", "model_context_window_exceeded":
		return Context
	case "request_too_large", "payload_too_large", "request_body_too_large":
		return Body
	case "image_too_large", "image_limit_exceeded":
		return Images
	default:
		return Unknown
	}
}
