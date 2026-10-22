package resetnotice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type MCP struct {
	Config Config
	Client *http.Client
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (m *MCP) call(ctx context.Context, session *string, protocol, method string, params any, id int) (json.RawMessage, error) {
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != 0 {
		body["id"] = id
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.Config.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid MCP request")
	}
	req.Header.Set("Authorization", m.Config.Authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if *session != "" {
		req.Header.Set("Mcp-Session-Id", *session)
	}
	if protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	client := m.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MCP transport failed; delivery may be unconfirmed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP HTTP status %d", resp.StatusCode)
	}
	if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
		*session = v
	}
	if id == 0 {
		return nil, nil
	}
	reader := io.LimitReader(resp.Body, 2<<20)
	var reply rpcResponse
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		var event strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				event.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				event.WriteByte('\n')
			}
			if line == "" && event.Len() > 0 {
				if json.Unmarshal([]byte(event.String()), &reply) == nil && string(reply.ID) == fmt.Sprint(id) {
					break
				}
				event.Reset()
			}
		}
	} else if err := json.NewDecoder(reader).Decode(&reply); err != nil {
		return nil, fmt.Errorf("invalid MCP response")
	}
	if string(reply.ID) != fmt.Sprint(id) || len(reply.Error) > 0 && string(reply.Error) != "null" || len(reply.Result) == 0 {
		return nil, fmt.Errorf("MCP request failed or returned no result")
	}
	return reply.Result, nil
}

func (m *MCP) Send(ctx context.Context, channel, message string) (string, error) {
	session := ""
	raw, err := m.call(ctx, &session, "", "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "subpool-reset-notice", "version": "1"}}, 1)
	if err != nil {
		return "", err
	}
	var init struct {
		Protocol string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &init) != nil || init.Protocol == "" {
		return "", fmt.Errorf("invalid MCP initialization")
	}
	if _, err = m.call(ctx, &session, init.Protocol, "notifications/initialized", map[string]any{}, 0); err != nil {
		return "", err
	}
	raw, err = m.call(ctx, &session, init.Protocol, "tools/call", map[string]any{"name": m.Config.Tool, "arguments": map[string]string{"channel_id": channel, "message": message}}, 2)
	if err != nil {
		return "", err
	}
	var result struct {
		IsError    bool                          `json:"isError"`
		Content    []struct{ Type, Text string } `json:"content"`
		Structured json.RawMessage               `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError {
		return "", fmt.Errorf("Slack MCP tool returned an error")
	}
	var receipt struct {
		Link string `json:"message_link"`
	}
	if json.Unmarshal(result.Structured, &receipt) == nil && receipt.Link != "" {
		return receipt.Link, nil
	}
	for _, c := range result.Content {
		if c.Type == "text" && json.Unmarshal([]byte(c.Text), &receipt) == nil && receipt.Link != "" {
			return receipt.Link, nil
		}
	}
	return "", fmt.Errorf("Slack delivery receipt unavailable; check channel before retrying")
}
