package tools

import "poisson/internal/provider"

// Re-export provider types so the tools package uses the canonical
// definitions from the provider package. This avoids duplicate type
// definitions and ensures the agent loop can pass Tool/ToolResult/ToolCall
// between packages without conversion.

type Tool = provider.Tool
type ToolResult = provider.ToolResult
type ToolCall = provider.ToolCall
type ToolDef = provider.ToolDef
