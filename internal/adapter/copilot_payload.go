package adapter

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/copilotpayload"
	"github.com/gesta-run/subpool/internal/domain"
)

// Keep upstream payload limits distinct from Subpool's inbound body limit.
func normalizeCopilotPayloadError(response *http.Response, request Request, endpoint string, inboundBytes, adaptedBytes int, adaptedBody ...[]byte) {
	if response == nil {
		return
	}
	reason := copilotpayload.Inspect(response)
	if response.StatusCode != http.StatusRequestEntityTooLarge && reason == copilotpayload.Unknown {
		return
	}
	bodyToMeasure := request.Body
	if len(adaptedBody) > 0 {
		bodyToMeasure = adaptedBody[0]
	}
	sizes := copilotpayload.Measure(bodyToMeasure)
	requestID := copilotDiagnosticToken(response.Header.Get("X-Request-Id"))
	if requestID == "" {
		requestID = copilotDiagnosticToken(response.Header.Get("X-Github-Request-Id"))
	}
	upstreamBytes := int64(-1)
	if response.Request != nil {
		// The transport sanitizes client metadata after protocol conversion.
		upstreamBytes = response.Request.ContentLength
		if requestID == "" {
			requestID = copilotDiagnosticToken(response.Request.Header.Get("X-Request-Id"))
		}
	}
	slog.Warn("Copilot upstream rejected request payload", "provider", domain.ProviderCopilot,
		"model", copilotDiagnosticToken(request.Model), "client_protocol", request.Kind,
		"upstream_endpoint", endpoint, "status", response.StatusCode, "upstream_request_id", requestID, "limit_reason", reason,
		"inbound_body_bytes", inboundBytes, "adapted_body_bytes", adaptedBytes, "upstream_body_bytes", upstreamBytes,
		"instructions_bytes", sizes.Instructions, "tools_bytes", sizes.Tools, "messages_bytes", sizes.Messages,
		"tool_output_bytes", sizes.ToolOutputs, "embedded_image_bytes", sizes.Images)

	// Never log or return upstream text: it may contain customer input.
	if response.Body != nil {
		_ = response.Body.Close()
	}
	code, message := "upstream_payload_too_large", "GitHub Copilot returned HTTP 413. Its response did not identify whether the request body, attachments, or model context exceeded a limit. Compact the conversation or reduce attachments before retrying; increasing Subpool's inbound body limit does not change upstream limits."
	if reason == copilotpayload.Context {
		// Codex recognizes this explicit code as recoverable context exhaustion.
		response.StatusCode = http.StatusBadRequest
		response.Status = "400 Bad Request"
		code, message = "context_length_exceeded", "GitHub Copilot reported that the model context limit was exceeded. Compact the conversation before retrying."
	} else if reason == copilotpayload.Body {
		message = "GitHub Copilot reported that the serialized request body is too large. Compact the conversation or reduce tool output and attachments before retrying."
	} else if reason == copilotpayload.Images {
		message = "GitHub Copilot reported that image limits were exceeded. Reduce image count or dimensions before retrying."
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": code, "message": message}})
	response.Header = response.Header.Clone()
	if response.Header == nil {
		response.Header = make(http.Header)
	}
	for _, name := range []string{"Content-Length", "Content-Encoding", "Transfer-Encoding", "Trailer",
		"ETag", "Last-Modified", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest", "Content-Range"} {
		response.Header.Del(name)
	}
	for _, name := range []string{"X-Request-Id", "X-Github-Request-Id"} {
		value := copilotDiagnosticToken(response.Header.Get(name))
		response.Header.Del(name)
		if value != "" {
			response.Header.Set(name, value)
		}
	}
	response.Header.Set("Content-Type", "application/json")
	response.Header.Set("X-Subpool-Error-Source", "upstream")
	if requestID != "" {
		response.Header.Set("X-Request-Id", requestID)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.TransferEncoding = nil
	response.Trailer = nil
	response.Uncompressed = false
}

// Accept only bounded identifiers, not arbitrary text from headers or input.
func copilotDiagnosticToken(value string) string {
	if len(value) > 256 {
		return ""
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return ""
	}
	return value
}
