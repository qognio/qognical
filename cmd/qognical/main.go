// Command qognical is the single binary for the Qognical booking server.
// Modes:
//
//	qognical serve            # default: start HTTP server (PocketBase + booking layer)
//	qognical healthcheck      # exit 0 if healthy (used by Docker HEALTHCHECK)
//	qognical genkey           # emit a base64 master key suitable for QOGNICAL_ENCRYPTION_KEY
//	qognical migrate          # PocketBase migrate sub-commands (passed through)
//
// Config comes from env vars (QOGNICAL_*) per docs/planning/08. Missing required
// vars cause the process to exit with a non-zero status — secure-by-default.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/spf13/cobra"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/google"
	"github.com/qognio/qognical/internal/adapters/jitsi"
	"github.com/qognio/qognical/internal/adapters/microsoft"
	"github.com/qognio/qognical/internal/adapters/msgraph"
	"github.com/qognio/qognical/internal/adapters/nextcloud"
	"github.com/qognio/qognical/internal/adapters/paypal"
	"github.com/qognio/qognical/internal/adapters/stripe"
	"github.com/qognio/qognical/internal/adapters/zoom"
	"github.com/qognio/qognical/internal/api"
	"github.com/qognio/qognical/internal/captcha"
	"github.com/qognio/qognical/internal/cli"
	"github.com/qognio/qognical/internal/config"
	"github.com/qognio/qognical/internal/cron"
	"github.com/qognio/qognical/internal/crypto"
	"github.com/qognio/qognical/internal/dsgvo"
	"github.com/qognio/qognical/internal/health"
	"github.com/qognio/qognical/internal/notifier"
	"github.com/qognio/qognical/internal/pipeline"
	"github.com/qognio/qognical/internal/ratelimit"
	"github.com/qognio/qognical/internal/spa"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/internal/svctoken"
	"github.com/qognio/qognical/internal/token"
	"github.com/qognio/qognical/internal/webhooks"

	_ "github.com/qognio/qognical/migrations"
)

// Version is set at build time via -ldflags="-X main.Version=...".
var Version = "dev"

func main() {
	// Sub-commands that don't need full config (genkey) handled early so an
	// operator can generate a key before they have one.
	if len(os.Args) >= 2 && os.Args[1] == "genkey" {
		runGenkey()
		return
	}

	// Strict config only when actually serving. Other subcommands (help,
	// version, migrate, healthcheck, superuser, ...) read what they need.
	dataDir := os.Getenv("QOGNICAL_DATA_DIR")
	if dataDir == "" {
		dataDir = "/pb_data"
	}
	if needsStrictConfig(os.Args[1:]) {
		cfg, err := config.LoadStrict()
		if err != nil {
			fmt.Fprintf(os.Stderr, "qognical: configuration error:\n%s\n", err)
			os.Exit(2)
		}
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "qognical: configuration invalid: %s\n", err)
			os.Exit(2)
		}
		if _, err := crypto.NewMaster(cfg.EncryptionKey); err != nil {
			fmt.Fprintf(os.Stderr, "qognical: QOGNICAL_ENCRYPTION_KEY invalid: %s\n", err)
			os.Exit(2)
		}
		dataDir = cfg.DataDir
		setupLogger(cfg)
	}

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir,
	})

	// Auto-run pending migrations on every start (idempotent by design).
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		Automigrate:  true,
		TemplateLang: migratecmd.TemplateLangGo,
	})

	// Wiring of the booking layer happens at OnServe so we have access to the
	// router. We also need a non-zero config; the serve-strict branch above
	// guarantees that, but defensive: only wire when the env was loaded.
	var (
		repo       *store.Repo
		pipe       *pipeline.Pipeline
		tokenSvc   *token.Service
		cfgGlobal  *config.Config
		master     *crypto.Master
		stripeProv adapters.PaymentProvider
		paypalProv adapters.PaymentProvider
		dispatcher *webhooks.Dispatcher
	)
	if needsStrictConfig(os.Args[1:]) {
		// Re-load (cheap; pure env reads). LoadStrict was already called above.
		cfgGlobal, _ = config.LoadStrict()
		master, _ = crypto.NewMaster(cfgGlobal.EncryptionKey)
		tokenSvc = token.New(master)
		repo = store.New(app)

		// Build the adapter registry: every provider package registers its
		// factory here once. CalendarFactory is host-scoped; payment
		// providers are instance-scoped (one Stripe key per qognical instance).
		registry := adapters.NewRegistry(repo, master)
		registry.RegisterCalendar(msgraph.Name, msgraph.Factory)
		registry.RegisterCalendar(microsoft.Name, microsoft.Factory)
		registry.RegisterCalendar(nextcloud.Name, nextcloud.Factory)
		registry.RegisterCalendar(google.Name, google.Factory)
		registry.RegisterMeeting(jitsi.Name, jitsi.Factory)
		registry.RegisterMeeting(zoom.Name, zoom.Factory)
		registry.RegisterPayment(stripe.Name, stripe.Factory)
		registry.RegisterPayment(paypal.Name, paypal.Factory)

		stripeProv = buildStripe(cfgGlobal, registry)
		paypalProv = buildPayPal(cfgGlobal, registry)
		dispatcher = webhooks.NewDispatcher(repo)
		pipe = pipeline.New(repo, tokenSvc, notifier.New(cfgGlobal), cfgGlobal.BaseURL).
			WithRegistry(registry).
			WithPayment(stripeProv, paypalProv).
			WithDispatcher(dispatcher)
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		health.Register(se, Version)
		if pipe != nil {
			readLim, _ := ratelimit.New(cfgGlobal.RateLimitPublic)
			mutLim, _ := ratelimit.New(cfgGlobal.RateLimitBook)
			apiInst := &api.API{
				Repo:               repo,
				Tokens:             tokenSvc,
				Pipeline:           pipe,
				Master:             master,
				CORSAllowedOrigins: cfgGlobal.CORSAllowedOrigins,
				Captcha:            captcha.New(cfgGlobal.CaptchaProvider, cfgGlobal.CaptchaSecret),
				ReadLimiter:        readLim,
				MutationLimiter:    mutLim,
			}
			apiInst.Register(se)
			apiInst.RegisterAdmin(se)
			apiInst.RegisterHost(se)
			apiInst.RegisterTeam(se)
			// Hosted Microsoft-365 browser OAuth flow (/oauth/microsoft/...).
			// Registered before spa.Register so the routes win over the SPA
			// catch-all. Uses the ONE dedicated Entra app from config.
			(&api.MSOAuth{
				App:          app,
				Repo:         repo,
				Master:       master,
				ClientID:     cfgGlobal.MSOAuth.ClientID,
				ClientSecret: cfgGlobal.MSOAuth.ClientSecret,
				Tenant:       cfgGlobal.MSOAuth.Tenant,
				BaseURL:      cfgGlobal.BaseURL,
				StateKey:     []byte(cfgGlobal.EncryptionKey),
			}).Register(se)
			(&webhooks.Inbound{
				Repo:   repo,
				Stripe: stripeProv,
				PayPal: paypalProv,
				OnPaymentResult: func(_ context.Context, ev adapters.WebhookEvent) error {
					return webhooks.DispatchPayment(repo, ev, pipe.OnPaymentSucceeded)
				},
			}).Register(se)
			spa.Register(se, cfgGlobal.BaseURL, cfgGlobal.OrgName)
		}
		return se.Next()
	})

	// Cron jobs: reminders + cleanup of expired pending payments + outbound
	// webhook delivery loop.
	if pipe != nil {
		app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}
			cron.Register(app, repo, pipe, notifier.New(cfgGlobal), cfgGlobal.BaseURL)
			if dispatcher != nil {
				app.Cron().MustAdd("outbound-webhooks", "* * * * *", func() {
					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
					defer cancel()
					dispatcher.RunDeliveries(ctx)
				})
			}
			return nil
		})
	}

	// Custom CLI commands.
	app.RootCmd.AddCommand(healthcheckCmd(app))
	app.RootCmd.AddCommand(dsgvo.ExportCmd(app))
	app.RootCmd.AddCommand(dsgvo.DeleteCmd(app))
	app.RootCmd.AddCommand(svctoken.Cmd(app))
	app.RootCmd.AddCommand(cli.IntegrationsCmd(app))

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// needsStrictConfig returns true when the invocation will run the HTTP
// server. We default to true (no subcommand = serve), and explicitly
// opt out for the well-known non-serving subcommands.
func needsStrictConfig(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "serve":
		return true
	case "-h", "--help", "help", "-v", "--version", "version",
		"healthcheck", "migrate", "superuser", "genkey":
		return false
	}
	// Unknown subcommand — let Cobra emit its error rather than us.
	return false
}

func runGenkey() {
	k, err := crypto.GenerateMasterKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(k)
}

func setupLogger(cfg *config.Config) {
	var lvl slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if strings.ToLower(cfg.LogFormat) == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// buildStripe constructs the Stripe adapter from QOGNICAL_STRIPE_* env vars.
// Returns nil when the secret key is empty (= integration off).
func buildStripe(cfg *config.Config, reg *adapters.Registry) adapters.PaymentProvider {
	if cfg.Providers.StripeSecretKey == "" {
		return nil
	}
	creds, _ := json.Marshal(stripe.Credentials{
		SecretKey:     cfg.Providers.StripeSecretKey,
		WebhookSecret: cfg.Providers.StripeWebhookSecret,
		APIVersion:    cfg.Providers.StripeAPIVersion,
	})
	prov, err := reg.PaymentForName(stripe.Name, creds, nil)
	if err != nil {
		slog.Warn("stripe init failed", "err", err)
		return nil
	}
	return prov
}

// buildPayPal mirrors buildStripe for PayPal.
func buildPayPal(cfg *config.Config, reg *adapters.Registry) adapters.PaymentProvider {
	if cfg.Providers.PayPalClientID == "" {
		return nil
	}
	creds, _ := json.Marshal(paypal.Credentials{
		ClientID:     cfg.Providers.PayPalClientID,
		ClientSecret: cfg.Providers.PayPalClientSecret,
		WebhookID:    cfg.Providers.PayPalWebhookID,
		Mode:         cfg.Providers.PayPalMode,
	})
	prov, err := reg.PaymentForName(paypal.Name, creds, nil)
	if err != nil {
		slog.Warn("paypal init failed", "err", err)
		return nil
	}
	return prov
}

// healthcheckCmd implements `qognical healthcheck` for Docker HEALTHCHECK.
// It bootstraps the app DB connection without starting the HTTP server,
// runs Check, prints JSON, and exits 0 / 1.
func healthcheckCmd(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Run local healthchecks and exit 0 if healthy",
		RunE: func(cmd *cobra.Command, args []string) error {
			// app.Bootstrap is called by PocketBase before any command runs,
			// so app.DB() is already wired here.
			r, err := health.Check(app, Version)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
			if errors.Is(err, health.ErrUnhealthy) {
				os.Exit(1)
			}
			return nil
		},
	}
}
