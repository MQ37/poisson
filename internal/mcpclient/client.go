// Package mcpclient is a minimal Model Context Protocol client for calling a
// single tool on a remote Streamable HTTP MCP server — just enough to drive
// Firecrawl's keyless endpoint (https://mcp.firecrawl.dev/v2/mcp), not a
// general-purpose SDK.
//
// The 2026-07-28 spec (see https://blog.modelcontextprotocol.io/posts/2026-07-28/)
// drops the initialize/initialized handshake and Mcp-Session-Id entirely —
// every request is self-contained. Firecrawl's live server was probed and
// only negotiates up to 2025-06-18/2025-11-25 (HTTP 400 on
// "2026-07-28": "Bad Request: Unsupported protocol version"), so this client
// speaks the older wire format. It still needs no handshake in practice:
// Firecrawl's anonymous/keyless tier accepts tools/call and tools/list with
// no prior initialize and no session ID at all — each call here is one POST,
// matching the new spec's spirit even though the version number hasn't
// caught up yet. Once Firecrawl advertises 2026-07-28, only protocolVersion
// above needs to change.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	protocolVersion = "2025-06-18"
	maxBytes        = 4 << 20 // 4 MiB: cap a tool response (OOM guard)
	errMaxBytes     = 4 << 10
)

// rpcRequest is a JSON-RPC 2.0 call, the only message shape this client
// sends — no batching, no notifications.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ToolResult is a tools/call reply: the content blocks a server returned,
// plus whether it flagged the call itself as an error (isError — distinct
// from a transport/protocol error, which surfaces as a Go error instead).
type ToolResult struct {
	Text    string // concatenated text of every {"type":"text"} content block
	IsError bool
}

// CallTool calls one tool on the MCP server at serverURL and returns its
// text content. No initialize handshake, no session tracking — see the
// package doc for why that's safe against Firecrawl's endpoint today.
func CallTool(ctx context.Context, serverURL, name string, arguments map[string]any) (ToolResult, error) {
	params := map[string]any{"name": name, "arguments": arguments}
	raw, err := call(ctx, serverURL, "tools/call", params)
	if err != nil {
		return ToolResult{}, err
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ToolResult{}, fmt.Errorf("decode tools/call result: %w", err)
	}

	var sb strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return ToolResult{Text: sb.String(), IsError: result.IsError}, nil
}

// call sends one JSON-RPC request and returns its "result" field, whether
// the server answered as plain application/json or as a single-event
// text/event-stream (Streamable HTTP's other allowed content type).
func call(ctx context.Context, serverURL string, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, errMaxBytes))
		return nil, fmt.Errorf("mcp HTTP %d: %s", resp.StatusCode, string(raw))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read mcp response: %w", err)
	}

	payload := data
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload, err = lastSSEData(data)
		if err != nil {
			return nil, err
		}
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(payload, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// lastSSEData extracts the "data:" payload from a Streamable HTTP SSE
// response. A stateless single-call server (Firecrawl's keyless tier) sends
// exactly one "message" event with the whole JSON-RPC reply as its data, so
// the last data line seen is the answer regardless of how many events
// preceded it (e.g. keep-alive comments).
func lastSSEData(raw []byte) (json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxBytes)
	var last string
	for scanner.Scan() {
		line := scanner.Text()
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			last = strings.TrimSpace(data)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan sse stream: %w", err)
	}
	if last == "" {
		return nil, fmt.Errorf("no data event in sse stream")
	}
	return json.RawMessage(last), nil
}
