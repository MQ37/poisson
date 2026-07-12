package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"poisson/internal/config"
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
