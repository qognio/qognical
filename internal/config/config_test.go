package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, envPrefix) {
			k := strings.SplitN(e, "=", 2)[0]
			os.Unsetenv(k)
		}
	}
}

func TestLoadStrictReportsAllMissing(t *testing.T) {
	clearEnv(t)
	_, err := LoadStrict()
	if err == nil {
		t.Fatal("expected error with empty env")
	}
	for _, want := range []string{"BASE_URL", "ENCRYPTION_KEY", "SMTP_HOST", "SMTP_FROM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message should mention %s, got: %s", want, err)
		}
	}
}

func TestLoadStrictHappyPath(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"QOGNICAL_BASE_URL":       "https://book.example.com",
		"QOGNICAL_ENCRYPTION_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"QOGNICAL_SMTP_HOST":      "smtp.example.com",
		"QOGNICAL_SMTP_USER":      "user",
		"QOGNICAL_SMTP_PASSWORD":  "secret",
		"QOGNICAL_SMTP_FROM":      "no-reply@example.com",
	} {
		os.Setenv(k, v)
	}
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("default SMTP port = %d, want 587", cfg.SMTP.Port)
	}
	if cfg.DataDir != "/pb_data" {
		t.Errorf("default DataDir = %q", cfg.DataDir)
	}
}

func TestSecretFromFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	os.WriteFile(keyFile, []byte("from-file-secret\n"), 0o600)
	os.Setenv("QOGNICAL_SMTP_PASSWORD_FILE", keyFile)

	got := getSecret("SMTP_PASSWORD")
	if got != "from-file-secret" {
		t.Errorf("got %q, want trimmed file contents", got)
	}
}

func TestCORSParsing(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"QOGNICAL_BASE_URL":             "https://x",
		"QOGNICAL_ENCRYPTION_KEY":       "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"QOGNICAL_SMTP_HOST":            "x",
		"QOGNICAL_SMTP_USER":            "x",
		"QOGNICAL_SMTP_PASSWORD":        "x",
		"QOGNICAL_SMTP_FROM":            "x@x",
		"QOGNICAL_CORS_ALLOWED_ORIGINS": "https://a.com, https://b.com ,  ",
	} {
		os.Setenv(k, v)
	}
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORSAllowedOrigins) != 2 {
		t.Fatalf("got %v, want 2 origins", cfg.CORSAllowedOrigins)
	}
}
