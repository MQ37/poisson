package guard

import (
	"regexp"
	"strings"
)

// redactedPlaceholder replaces a detected secret value verbatim — no key
// name or hint kept, so a false negative elsewhere can't reconstruct
// anything from the placeholder itself.
const redactedPlaceholder = "[REDACTED]"

// RedactSecrets scans arbitrary text (tool output, spilled files, anything
// about to reach the model, the TUI, or the session store) and replaces
// secret-shaped substrings with redactedPlaceholder. Best-effort, not a
// guarantee: a command like `widget-cli secrets download --format env` can
// print real credentials straight to stdout, and everything downstream
// (model context sent to a remote provider, on-disk session store, /tmp
// spill files) is a leak vector once that text is in play. This is the one
// choke point (see tools.TrimToolResult) all of those sinks share.
//
// Two independent layers, both regexp-only (no dependency):
//   - vendor token shapes (AWS key IDs, GitHub/Slack/Stripe/OpenAI/Google
//     tokens, PEM private keys, JWTs, embedded URL credentials, Bearer
//     headers) — fire regardless of surrounding context.
//   - "KEY=VALUE" / "KEY: VALUE" pairs whose KEY name looks secret-related
//     (contains secret/token/password/credential/bearer/auth/api-key/...) —
//     catches env-dump-style output like `WIDGET_API_TOKEN="wgt_live_x"`
//     whose value has no recognizable vendor shape of its own.
//
// Deliberately biased toward over-redaction: a key merely named like
// AUTHOR_NAME can get masked even though it holds no secret, which is
// noise, not a leak — the failure mode this trades against (a real
// credential slipping through untouched) is the one that actually matters.
func RedactSecrets(s string) string {
	for _, re := range vendorTokenPatterns {
		s = re.ReplaceAllString(s, redactedPlaceholder)
	}
	s = pemPrivateKeyRe.ReplaceAllString(s, redactedPlaceholder)
	s = bearerHeaderRe.ReplaceAllString(s, "${1}"+redactedPlaceholder)
	s = urlCredentialsRe.ReplaceAllString(s, "://"+redactedPlaceholder+"@")
	s = redactKeyValuePairs(s)
	return s
}

// vendorTokenPatterns match known credential shapes with a fixed, recognizable
// prefix — precise enough to redact unconditionally, with no surrounding
// KEY= context needed.
var vendorTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                  // AWS access key ID
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),        // GitHub token
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),      // Slack token
	regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}\b`), // Stripe key
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),             // OpenAI / Anthropic-style key
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),             // Google API key
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), // JWT
	regexp.MustCompile(`\bapify_api_[A-Za-z0-9]{20,}\b`),        // Apify API token
}

// pemPrivateKeyRe matches a full PEM private-key block, header to footer.
var pemPrivateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)

// bearerHeaderRe keeps the "Bearer " prefix (harmless, common in log lines)
// and redacts only the token that follows.
var bearerHeaderRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{10,}`)

// urlCredentialsRe matches userinfo embedded in a URL, e.g.
// mongodb://user:pass@host — the whole user:pass segment is dropped, not
// just masked, since the username here is itself often sensitive.
var urlCredentialsRe = regexp.MustCompile(`://[^\s/:@]+:[^\s/@]+@`)

// kvPairRe matches a bare identifier followed by an assignment and a value
// token — env-dump lines (KEY="value"), YAML (key: value), and simple JSON
// ("key": "value") all take this shape. Whether the identifier is actually
// secret-related is decided separately by isSecretKeyName; this regex only
// finds candidates.
var kvPairRe = regexp.MustCompile(`(?i)"?([A-Za-z_][\w.-]*)"?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)

// camelBoundaryRe inserts a split point before an uppercase letter that
// follows a lowercase/digit, so "apiKey" normalizes to "api_key" — the same
// shape isSecretKeyName's substring check already recognizes for the
// snake_case/SCREAMING_CASE forms env dumps actually use.
var camelBoundaryRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// secretKeywords are substrings whose presence in an identifier (after
// case-folding) marks the identifier as secret-related. Substring, not
// whole-word: catches "apiKey"/"api_key"/"APIKEY" and compounds like
// "client_secret" alike without a bespoke rule per naming convention.
var secretKeywords = []string{
	"secret", "token", "password", "passwd", "credential", "bearer",
	"auth", "apikey", "api_key", "accesskey", "access_key",
	"privatekey", "private_key",
}

func isSecretKeyName(key string) bool {
	k := strings.ToLower(camelBoundaryRe.ReplaceAllString(key, "${1}_${2}"))
	for _, kw := range secretKeywords {
		if strings.Contains(k, kw) {
			return true
		}
	}
	return false
}

// secretVarNameWords are whole-word (underscore-split) matches for a shell
// variable *name*, used by looksLikeSecretVarName — the pre-execution
// bash-guard check (printsSecretShapedVar) that decides whether echo/cat get
// to skip approval entirely, not the RedactSecrets text scan above. Bare
// "key" is deliberately included here (unlike secretKeywords, which skips it
// to avoid over-redacting output text like PRIMARY_KEY/FOREIGN_KEY): a false
// positive here just costs one extra classifier call, while a false negative
// costs a real credential (e.g. SOLANA_TESTING_KEY) sailing through the
// SAFE-list fast path with zero review — the two checks trade off false
// positives differently because a deny-gate miss is far more expensive than
// a redaction-text miss.
var secretVarNameWords = map[string]bool{
	"secret": true, "token": true, "password": true, "passwd": true,
	"pass": true, "credential": true, "credentials": true, "bearer": true,
	"auth": true, "key": true,
}

// looksLikeSecretVarName reports whether a shell variable name (already
// extracted, no leading $/{}) looks credential-related — split into
// underscore-delimited words (env var convention is SCREAMING_SNAKE_CASE,
// so no camelCase normalization is needed here unlike isSecretKeyName)
// and matched whole-word against secretVarNameWords.
func looksLikeSecretVarName(name string) bool {
	for _, part := range strings.Split(strings.ToLower(name), "_") {
		if secretVarNameWords[part] {
			return true
		}
	}
	return false
}

// extractVarRefName finds a $NAME or ${NAME} shell variable reference
// inside a raw token (quotes, if any, still attached — tokenize() keeps
// them) and returns NAME. Best-effort: doesn't require a closing "}" for
// the braced form, since the token is already known-bounded.
func extractVarRefName(tok string) (string, bool) {
	i := strings.IndexByte(tok, '$')
	if i < 0 || i+1 >= len(tok) {
		return "", false
	}
	rest := tok[i+1:]
	rest = strings.TrimPrefix(rest, "{")
	j := 0
	for j < len(rest) {
		c := rest[j]
		isNameByte := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(j > 0 && c >= '0' && c <= '9')
		if !isNameByte {
			break
		}
		j++
	}
	if j == 0 {
		return "", false
	}
	return rest[:j], true
}

// printsSecretShapedVar reports whether any argument of a printer command
// (echo/cat — see checkPerCommandDetectors) references a shell variable
// whose name looks secret-related, e.g. `echo "$AWS_SECRET_ACCESS_KEY"` or
// `echo "$SOLANA_TESTING_KEY"`. tokens[0] (the command name itself) is
// skipped.
func printsSecretShapedVar(tokens []string) (string, bool) {
	for _, tok := range tokens[1:] {
		if name, ok := extractVarRefName(tok); ok && looksLikeSecretVarName(name) {
			return name, true
		}
	}
	return "", false
}

// redactKeyValuePairs replaces the value half of every KEY=VALUE/KEY: VALUE
// match whose key name looks secret-related, leaving the key and every
// non-matching pair untouched.
func redactKeyValuePairs(s string) string {
	matches := kvPairRe.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		keyStart, keyEnd := m[2], m[3]
		valStart, valEnd := m[4], m[5]
		b.WriteString(s[last:fullStart])
		if isSecretKeyName(s[keyStart:keyEnd]) {
			b.WriteString(s[fullStart:valStart])
			val := s[valStart:valEnd]
			if len(val) > 0 && (val[0] == '"' || val[0] == '\'') {
				q := val[0]
				b.WriteByte(q)
				b.WriteString(redactedPlaceholder)
				b.WriteByte(q)
			} else {
				b.WriteString(redactedPlaceholder)
			}
		} else {
			b.WriteString(s[fullStart:fullEnd])
		}
		last = fullEnd
	}
	b.WriteString(s[last:])
	return b.String()
}
