package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"
	"sync"

	"github.com/mq37/poisson/internal/config"
)

// applyStealth transforms the request to look like a genuine Claude Code
// client. Only called when auth is OAuth.
//
// 1. Sanitize system blocks (remove fingerprint paragraphs, inline replacements)
// 2. Insert CC identity as system[0]
// 3. Compute billing header, prepend as system[0]
// Final: [billing_header, cc_identity, actual_system_prompt(s)]
func (p *AnthropicProvider) applyStealth(req *Request) {
	var cfg config.StealthConfig
	if p.config != nil {
		cfg = p.config.Stealth
	} else {
		cfg = config.DefaultStealthConfig()
	}

	// 1. Sanitize system blocks (skip index 0 if it's the CC identity marker).
	sanitized := make([]SystemBlock, 0, len(req.System))
	for i, sb := range req.System {
		if i == 0 && strings.Contains(sb.Text, "You are Claude Code") {
			continue // skip pi-style identity marker
		}
		sanitized = append(sanitized, SystemBlock{
			Text:     sanitizeSystemText(sb.Text, cfg),
			CacheCtl: sb.CacheCtl,
		})
	}

	// 2. Insert CC identity.
	identity := SystemBlock{Text: claudeCodeIdentity}

	// 3. Compute billing header from first user message.
	firstUserText := extractFirstUserText(req.Messages)
	billingText := buildBillingHeaderValue(firstUserText, cfg)
	billing := SystemBlock{Text: billingText} // no cache_control — changes per request

	// Final order: [billing, identity, ...sanitized]
	req.System = append([]SystemBlock{billing, identity}, sanitized...)
}

// claudeCodeIdentity is the real Claude Code identity text.
const claudeCodeIdentity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."

// extractFirstUserText gets the text of the first user message.
func extractFirstUserText(messages []Message) string {
	for _, msg := range messages {
		if msg.Role == "user" {
			for _, cb := range msg.Content {
				if cb.Type == "text" && cb.Text != "" {
					return cb.Text
				}
			}
		}
	}
	return ""
}

// computeCCH returns the first 5 hex chars of SHA-256(text).
func computeCCH(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:5]
}

// computeVersionSuffix returns the 3-char version suffix.
func computeVersionSuffix(text string, cfg config.StealthConfig) string {
	chars := make([]byte, len(cfg.CCHPositions))
	for i, pos := range cfg.CCHPositions {
		if pos < len(text) {
			chars[i] = text[pos]
		} else {
			chars[i] = '0'
		}
	}
	h := sha256.Sum256([]byte(cfg.CCHSalt + string(chars) + cfg.CCVersion))
	return hex.EncodeToString(h[:])[:3]
}

// buildBillingHeaderValue constructs the billing header text block.
func buildBillingHeaderValue(firstUserText string, cfg config.StealthConfig) string {
	cch := computeCCH(firstUserText)
	suffix := computeVersionSuffix(firstUserText, cfg)
	return "x-anthropic-billing-header: cc_version=" + cfg.CCVersion + "." + suffix +
		"; cc_entrypoint=" + cfg.CCEntrypoint + "; cch=" + cch + ";"
}

// paragraphRemovalAnchors identifies paragraphs to strip from the system prompt.
var paragraphRemovalAnchors = []string{
	"github.com/badlogic/pi-mono",
	"github.com/badlogic/cchistory",
	"operating inside pi",
	"Packages documentation (read only",
	"packages/coding-agent",
	"@mariozechner",
	"Poisson documentation",
	"operating inside Poisson",
}

// inlineReplacements are applied after paragraph removal.
var inlineReplacements = []struct{ match, replacement string }{
	{"if pi honestly", "if the assistant honestly"},
	{"if Poisson honestly", "if the assistant honestly"},
	{"Here is some useful information about the environment you are running in:",
		"Environment context you are running in:"},
}

// stealthBetaHeader builds the anthropic-beta header value observed on real
// Claude Code's main agentic-loop /v1/messages traffic (cc-sniff captures
// 0012/0014/0015: tools + adaptive thinking). effort-2025-11-24 is inserted
// only when this request actually uses adaptive thinking, matching capture
// 0011 (a plain, tool-less thinking:disabled call), which omits it along
// with most of the rest of the list — see setHeaders' comment on
// adaptiveEffort. Poisson's requests almost always carry tools, so this list
// targets that majority case rather than replicating every per-feature
// variant Anthropic's own client toggles.
func stealthBetaHeader(adaptiveEffort bool) string {
	betas := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"context-1m-2025-08-07",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"mid-conversation-system-2026-04-07",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
	}
	if adaptiveEffort {
		betas = append(betas, "effort-2025-11-24")
	}
	betas = append(betas, "extended-cache-ttl-2025-04-11", "cache-diagnosis-2026-04-07")
	return strings.Join(betas, ",")
}

// stealthSessionID is a per-process UUID sent as X-Claude-Code-Session-Id,
// mimicking the id real Claude Code generates once at CLI startup and
// reuses for every request in that run (cc-sniff captures 0011-0015 all
// share one session id across many requests).
var stealthSessionID = sync.OnceValue(newUUIDv4)

// stealthOS and stealthArch report the running platform in the casing
// Anthropic's bundled Node/Stainless SDK uses for its own X-Stainless-OS /
// X-Stainless-Arch headers.
func stealthOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

func stealthArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	case "386":
		return "x32"
	default:
		return "x64"
	}
}

// toolNamePrefix camouflages tool names as Claude Code's MCP-tool naming
// convention. Ported from opencode-anthropic-auth's transform.ts, which
// found bare lowercase tool names ("bash", "read", ...) — exactly poisson's
// own tool names — get flagged as a non-Claude-Code client.
const toolNamePrefix = "mcp_"

// prefixToolName maps a bare tool name to its wire form: bash -> mcp_Bash.
func prefixToolName(name string) string {
	if name == "" {
		return name
	}
	return toolNamePrefix + strings.ToUpper(name[:1]) + name[1:]
}

// unprefixToolName reverses prefixToolName for tool_use blocks arriving from
// the API. Names without the prefix pass through unchanged (defensive: this
// should never happen given prefixToolName is applied to every outgoing
// tool, but a silent no-op is safer than mangling an unexpected name).
func unprefixToolName(name string) string {
	rest, ok := strings.CutPrefix(name, toolNamePrefix)
	if !ok || rest == "" {
		return name
	}
	return strings.ToLower(rest[:1]) + rest[1:]
}

// sanitizeSystemText removes fingerprint paragraphs and applies inline replacements.
func sanitizeSystemText(text string, cfg config.StealthConfig) string {
	paragraphs := strings.Split(text, "\n\n")

	filtered := make([]string, 0, len(paragraphs))
	for _, para := range paragraphs {
		drop := false
		for _, anchor := range paragraphRemovalAnchors {
			if strings.Contains(para, anchor) {
				drop = true
				break
			}
		}
		if !drop {
			filtered = append(filtered, para)
		}
	}

	result := strings.Join(filtered, "\n\n")

	for _, rule := range inlineReplacements {
		result = strings.ReplaceAll(result, rule.match, rule.replacement)
	}

	return strings.TrimSpace(result)
}
