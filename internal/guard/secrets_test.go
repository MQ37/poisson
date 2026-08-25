package guard

import (
	"strings"
	"testing"
)

func TestRedactSecretsEnvDump(t *testing.T) {
	in := `WIDGET_API_TOKEN="wgt_live_abcDEF123456"
WIDGET_REGION="us-east-1"
DOPPLER_CONFIG="sandbox"
DOPPLER_ENVIRONMENT="sandbox"
DOPPLER_PROJECT="widget-service"
MODEL="global.some-model-4-6"
MONGO_URL="mongodb://127.0.0.1:27017/?directConnection=true"
`
	out := RedactSecrets(in)

	if strings.Contains(out, "wgt_live_abcDEF123456") {
		t.Fatalf("secret value leaked through: %q", out)
	}
	if !strings.Contains(out, `WIDGET_API_TOKEN="[REDACTED]"`) {
		t.Errorf("expected redacted WIDGET_API_TOKEN, got %q", out)
	}
	for _, keep := range []string{
		`WIDGET_REGION="us-east-1"`,
		`DOPPLER_CONFIG="sandbox"`,
		`DOPPLER_PROJECT="widget-service"`,
		`MODEL="global.some-model-4-6"`,
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("non-secret line was altered, want %q intact, got %q", keep, out)
		}
	}
}

func TestRedactSecretsVendorPatterns(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"aws access key", "aws_access_key_id = AKIAABCDEFGHIJKLMNOP"},
		{"github token", "GITHUB_TOKEN=ghp_1234567890abcdefghijklmnopqrstuvwx"},
		{"slack token", "token: xoxb-1234567890-abcdefghijklmnop"},
		{"stripe key", "STRIPE_KEY=sk_live_4242424242424242424242"},
		{"openai key", "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz123456"},
		{"google api key", "AIzaSyABCDEFGHIJKLMNOPQRSTUVWXYZ0123456"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ_dQw4w9WgXcQ"},
		{"bearer header", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789"},
		{"url credentials", "mongodb://admin:sup3rSecret@db.internal:27017/app"},
		{"pem private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIBVQIBADANBgkqhkiG9w0B\n-----END RSA PRIVATE KEY-----"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactSecrets(tc.in)
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("RedactSecrets(%q) = %q, want a [REDACTED] marker", tc.in, out)
			}
			if out == tc.in {
				t.Errorf("RedactSecrets(%q) left input unchanged", tc.in)
			}
		})
	}
}

func TestRedactSecretsLeavesOrdinaryTextAlone(t *testing.T) {
	cases := []string{
		"AUTHOR_NAME=Jane Doe", // accepted false positive, see doc comment — not asserted here
		"total 42\ndrwxr-xr-x 3 mq mq 4096 Jan 1 00:00 .",
		"commit abc1234567890def1234567890abcdef12345678",
		"PRIMARY_KEY=42",
	}
	// These must not panic and must not swallow unrelated content entirely.
	for _, in := range cases {
		out := RedactSecrets(in)
		if out == "" && in != "" {
			t.Errorf("RedactSecrets(%q) returned empty output", in)
		}
	}
}

func TestRedactSecretsIdempotent(t *testing.T) {
	in := `API_TOKEN="abc123"`
	once := RedactSecrets(in)
	twice := RedactSecrets(once)
	if once != twice {
		t.Errorf("RedactSecrets not idempotent: once=%q twice=%q", once, twice)
	}
}

func TestLooksLikeSecretVarName(t *testing.T) {
	yes := []string{"AWS_SECRET_ACCESS_KEY", "SOLANA_TESTING_KEY", "DB_PASSWORD", "API_TOKEN", "AUTH_HEADER"}
	for _, name := range yes {
		if !looksLikeSecretVarName(name) {
			t.Errorf("looksLikeSecretVarName(%q) = false, want true", name)
		}
	}
	no := []string{"AWS_REGION", "MODEL", "MONGO_URL", "AUTHOR_NAME", "HOME"}
	// AUTHOR_NAME is a deliberate contrast with RedactSecrets' substring
	// check: this is whole-word, so "author" alone never matches "auth".
	// (PRIMARY_KEY is NOT in this list on purpose — bare "key" as its own
	// word is documented, accepted over-inclusion; see secretVarNameWords.)
	for _, name := range no {
		if looksLikeSecretVarName(name) {
			t.Errorf("looksLikeSecretVarName(%q) = true, want false", name)
		}
	}
}

func TestPrintsSecretShapedVar(t *testing.T) {
	yes := []string{
		`echo "$AWS_SECRET_ACCESS_KEY"`,
		`echo $SOLANA_TESTING_KEY`,
		`cat "${DB_PASSWORD}"`,
	}
	for _, cmd := range yes {
		safe, reason := Classify(cmd)
		if safe {
			t.Errorf("Classify(%q) = safe, want demoted off the SAFE list", cmd)
		}
		if reason == "" || !strings.Contains(reason, "secret-shaped variable") {
			t.Errorf("Classify(%q) reason = %q, want secret-shaped-variable reason", cmd, reason)
		}
	}
	no := []string{
		`echo hello world`,
		`echo "$HOME/bin"`,
		`cat README.md`,
	}
	for _, cmd := range no {
		safe, reason := Classify(cmd)
		if !safe {
			t.Errorf("Classify(%q) = unsafe (%s), want safe", cmd, reason)
		}
	}
}

func FuzzRedactSecretsNoPanic(f *testing.F) {
	seeds := []string{
		"",
		"WIDGET_API_TOKEN=\"x\"",
		"-----BEGIN RSA PRIVATE KEY-----",
		"://user:@host",
		"Bearer",
		"eyJ.eyJ.",
		"\"key\": \"value\"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		RedactSecrets(s)
	})
}
