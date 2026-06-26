// Package provider defines the streaming provider abstraction used by the
// Poisson agent loop. Concrete implementations (Ollama, Anthropic, xAI) live
// alongside this file in the same package.
//
// The core type is the Provider interface, which exposes model listing and a
// streaming completion endpoint (Stream). Stream returns a channel of
// StreamEvent values; see the channel lifecycle contract on Stream for the
// guaranteed close semantics.
package provider

import (
	"context"
	"encoding/json"
)

// Provider is the abstraction every LLM backend implements.
type Provider interface {
	// ID returns a short, stable identifier for this provider
	// (e.g. "ollama", "anthropic", "xai").
	ID() string

	// Stream issues a streaming completion request to the provider and
	// returns a channel from which the caller drains StreamEvent values.
	//
	// Channel lifecycle contract:
	//   - The channel is always closed. The producer goroutine uses
	//     `defer close(ch)`.
	//   - EventDone or EventError is always the last event before close
	//     (exactly one of them is emitted on the normal path).
	//   - On ctx.Cancel() the producer closes the channel immediately,
	//     without emitting EventDone or EventError.
	//   - If the HTTP request itself fails (connection refused, non-2xx
	//     status before streaming starts, etc.) Stream returns (nil, err)
	//     and no channel is created.
	//   - The caller must `range` over the channel and stop when it closes.
	Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error)

	// Models returns the models available to this provider. Used by
	// `Poisson --model` tab-completion and the `/model` command. If the
	// provider cannot enumerate models, it returns the configured model
	// only (never an error for the common case).
	Models() ([]Model, error)
}

// Request is the provider-agnostic completion request assembled by the agent
// loop. Providers map it onto their own wire format.
type Request struct {
	Model       string        // provider-specific model identifier
	System      []SystemBlock // ordered system blocks
	Messages    []Message
	Tools       []ToolDef
	MaxTokens   int      // 0 = provider default
	Temperature *float64 // nil = provider default
	Effort      string   // thinking effort: "" | "low" | "medium" | "high" | "xhigh" | "max"
}

// SystemBlock is one block of system-prompt text. CacheCtl is an
// Anthropic-only hint ("ephemeral" or ""); other providers ignore it.
type SystemBlock struct {
	Text     string
	CacheCtl string // "ephemeral" or "" — Anthropic-only, ignored by others
}

// Message is one conversational turn. Role is "user", "assistant", or
// "tool". Content is an ordered list of typed blocks.
type Message struct {
	Role    string
	Content []ContentBlock
}

// ContentBlock is a typed piece of message content.
//
// Type is one of "text", "tool_use", or "tool_result". Only the fields
// relevant to the block type are populated:
//
//   - text:        Text
//   - tool_use:    ToolCallID, ToolName, ToolInput (json.RawMessage)
//   - tool_result: ToolCallID, ToolResult
//   - thinking:    Thinking, ThinkingSignature, Redacted (Anthropic extended thinking)
type ContentBlock struct {
	Type       string          // text | tool_use | tool_result | thinking
	Text       string          // text blocks
	ToolCallID string          // tool_use + tool_result
	ToolName   string          // tool_use
	ToolInput  json.RawMessage // tool_use
	ToolResult string          // tool_result

	// Anthropic extended-thinking fields (Type == "thinking"). Thinking holds
	// the reasoning text; ThinkingSignature is the opaque signature that must
	// be replayed verbatim; Redacted marks a redacted_thinking block whose
	// ThinkingSignature carries the encrypted payload.
	Thinking          string
	ThinkingSignature string
	Redacted          bool
}

// StreamEventType identifies the kind of a StreamEvent.
type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventToolUseStart
	EventToolUseDelta
	EventToolUseStop
	EventThinkingDelta     // Text carries a thinking text delta
	EventThinkingSignature // Text carries a thinking signature delta
	EventThinkingRedacted  // Text carries the opaque redacted_thinking payload
	EventDone
	EventError
)

// StreamEvent is one item on the Stream channel. The fields populated depend
// on Type:
//
//   - EventTextDelta:       Text
//   - EventToolUseStart:    ToolCall (ID + Name + Input populated)
//   - EventToolUseDelta:    ToolCall (Input carries the incremental args)
//   - EventToolUseStop:     ToolCall (final Input)
//   - EventDone:            Usage (exact token counts from the provider)
//   - EventError:           Error
type StreamEvent struct {
	Type     StreamEventType
	Text     string
	ToolCall *ToolCall
	Error    error
	Usage    *Usage // exact token counts from the provider (on EventDone)
}

// Usage holds exact token counts reported by the provider for one API call.
// Cache fields are zero for providers that do not expose prompt caching.
type Usage struct {
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	InputTokensUnknown bool
}

// AnthropicUsage is kept for tests/compatibility; Usage now carries cache
// fields directly so callers do not need type assertions.
type AnthropicUsage struct {
	Usage
}

// Tool is the interface every Poisson tool implements.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's input parameters
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolResult is the outcome of a tool execution. Exactly one of Content or
// Error is meaningful: Error is the empty string on success.
type ToolResult struct {
	Content string
	Error   string // empty if success
}

// ToolDef is a tool definition serialized into the provider request. It is
// the wire form of a Tool (Name + Description + Schema).
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// ToolCall is a tool invocation produced by the model during streaming.
// Input is the raw JSON arguments object from the provider.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Model describes one model available from a provider.
type Model struct {
	ID            string
	Name          string
	ContextWindow int
}
