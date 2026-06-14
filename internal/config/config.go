// Package config loads runtime configuration from environment variables
// and "_FILE" variants (Docker-secret friendly). Required values cause
// LoadStrict to return an error so the binary fails fast at startup
// rather than starting with an insecure or broken default.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const envPrefix = "QOGNICAL_"

// Config is the resolved runtime configuration.
type Config struct {
	BaseURL       string
	ListenAddr    string
	DataDir       string
	EncryptionKey string
	OrgName       string // shown on landing page / browser tab; defaults to "Terminbuchung"

	SMTP SMTPConfig

	LogLevel  string
	LogFormat string

	CORSAllowedOrigins []string
	RateLimitPublic    string // "60/min"
	RateLimitBook      string // "5/min"

	CaptchaProvider string
	CaptchaSiteKey  string
	CaptchaSecret   string

	RetentionBookingsDays      int
	RetentionAuditDays         int
	RetentionNotificationsDays int

	Providers ProviderConfig
}

// SMTPConfig groups SMTP fields. Required for the notifier.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// ProviderConfig holds opt-in integration credentials. Empty values mean
// the corresponding adapter is disabled.
type ProviderConfig struct {
	MSGraphTenantID     string
	MSGraphClientID     string
	MSGraphClientSecret string

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURI  string

	StripeSecretKey     string
	StripeWebhookSecret string
	StripeAPIVersion    string

	PayPalClientID     string
	PayPalClientSecret string
	PayPalMode         string
	PayPalWebhookID    string

	JitsiBaseURL   string
	JitsiJWTSecret string
	JitsiJWTAppID  string
}

// LoadStrict reads configuration from os environment. It returns an error
// listing every missing required value, so the operator sees all problems
// at once rather than fixing them one by one.
func LoadStrict() (*Config, error) {
	cfg := &Config{
		ListenAddr: getDefault("LISTEN_ADDR", "0.0.0.0:8090"),
		DataDir:    getDefault("DATA_DIR", "/pb_data"),
		LogLevel:   getDefault("LOG_LEVEL", "info"),
		LogFormat:  getDefault("LOG_FORMAT", "json"),

		RateLimitPublic: getDefault("RATE_LIMIT_PUBLIC", "60/min"),
		RateLimitBook:   getDefault("RATE_LIMIT_BOOK", "5/min"),

		OrgName: getDefault("ORG_NAME", "Terminbuchung"),

		CaptchaProvider: get("CAPTCHA_PROVIDER"),
		CaptchaSiteKey:  get("CAPTCHA_SITE_KEY"),
		CaptchaSecret:   getSecret("CAPTCHA_SECRET"),
	}

	var missing []string

	cfg.BaseURL = get("BASE_URL")
	if cfg.BaseURL == "" {
		missing = append(missing, "QOGNICAL_BASE_URL")
	}

	cfg.EncryptionKey = getSecret("ENCRYPTION_KEY")
	if cfg.EncryptionKey == "" {
		missing = append(missing, "QOGNICAL_ENCRYPTION_KEY (or QOGNICAL_ENCRYPTION_KEY_FILE)")
	}

	cfg.SMTP.Host = get("SMTP_HOST")
	if cfg.SMTP.Host == "" {
		missing = append(missing, "QOGNICAL_SMTP_HOST")
	}
	cfg.SMTP.Port = getInt("SMTP_PORT", 587)
	cfg.SMTP.User = get("SMTP_USER")
	if cfg.SMTP.User == "" {
		missing = append(missing, "QOGNICAL_SMTP_USER")
	}
	cfg.SMTP.Password = getSecret("SMTP_PASSWORD")
	if cfg.SMTP.Password == "" {
		missing = append(missing, "QOGNICAL_SMTP_PASSWORD (or QOGNICAL_SMTP_PASSWORD_FILE)")
	}
	cfg.SMTP.From = get("SMTP_FROM")
	if cfg.SMTP.From == "" {
		missing = append(missing, "QOGNICAL_SMTP_FROM")
	}

	if origins := get("CORS_ALLOWED_ORIGINS"); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.CORSAllowedOrigins = append(cfg.CORSAllowedOrigins, o)
			}
		}
	}

	cfg.RetentionBookingsDays = getInt("DATA_RETENTION_BOOKINGS_DAYS", 0)
	cfg.RetentionAuditDays = getInt("DATA_RETENTION_AUDIT_DAYS", 90)
	cfg.RetentionNotificationsDays = getInt("DATA_RETENTION_NOTIFICATIONS_DAYS", 365)

	cfg.Providers = ProviderConfig{
		MSGraphTenantID:     get("MSGRAPH_TENANT_ID"),
		MSGraphClientID:     get("MSGRAPH_CLIENT_ID"),
		MSGraphClientSecret: getSecret("MSGRAPH_CLIENT_SECRET"),

		GoogleClientID:     get("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: getSecret("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURI:  get("GOOGLE_REDIRECT_URI"),

		StripeSecretKey:     getSecret("STRIPE_SECRET_KEY"),
		StripeWebhookSecret: getSecret("STRIPE_WEBHOOK_SECRET"),
		StripeAPIVersion:    get("STRIPE_API_VERSION"),

		PayPalClientID:     get("PAYPAL_CLIENT_ID"),
		PayPalClientSecret: getSecret("PAYPAL_CLIENT_SECRET"),
		PayPalMode:         getDefault("PAYPAL_MODE", "sandbox"),
		PayPalWebhookID:    get("PAYPAL_WEBHOOK_ID"),

		JitsiBaseURL:   get("JITSI_BASE_URL"),
		JitsiJWTSecret: getSecret("JITSI_JWT_SECRET"),
		JitsiJWTAppID:  get("JITSI_JWT_APP_ID"),
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars:\n  - %s",
			strings.Join(missing, "\n  - "))
	}

	return cfg, nil
}

// get returns the value of QOGNICAL_<name>, or "" if unset.
func get(name string) string {
	return os.Getenv(envPrefix + name)
}

// getDefault returns the env value or fallback.
func getDefault(name, fallback string) string {
	if v := get(name); v != "" {
		return v
	}
	return fallback
}

// getInt parses an int env var, returns fallback on error.
func getInt(name string, fallback int) int {
	v := get(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// getSecret reads either QOGNICAL_<name> or the contents of the file at
// QOGNICAL_<name>_FILE. _FILE takes precedence to support Docker / k8s secrets.
func getSecret(name string) string {
	if path := get(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimRight(string(b), "\r\n")
		}
	}
	return get(name)
}

// Validate runs cross-field sanity checks after LoadStrict has succeeded.
func (c *Config) Validate() error {
	if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
		return fmt.Errorf("SMTP port out of range: %d", c.SMTP.Port)
	}
	if !strings.HasPrefix(c.BaseURL, "https://") && !strings.HasPrefix(c.BaseURL, "http://") {
		return errors.New("QOGNICAL_BASE_URL must include scheme (http:// or https://)")
	}
	return nil
}
