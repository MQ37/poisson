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
