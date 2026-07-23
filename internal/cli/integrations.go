// Package cli holds operator CLI subcommands that share access to the
// repository + encryption key. Integration setup is the canonical use-case:
// the host's calendar credentials must be AES-GCM-encrypted before they go
// into the database, so the PocketBase admin UI alone isn't enough.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"

	"github.com/qognio/qognical/internal/config"
	"github.com/qognio/qognical/internal/crypto"
	"github.com/qognio/qognical/migrations"
)

// IntegrationsCmd returns the `qognical integrations` command tree.
func IntegrationsCmd(app core.App) *cobra.Command {
	c := &cobra.Command{
		Use:   "integrations",
		Short: "Manage per-host calendar/meeting/payment integrations",
	}
	c.AddCommand(setCmd(app), listCmd(app), removeCmd(app))
	return c
}

func setCmd(app core.App) *cobra.Command {
	var owner, provider, credsFile, configFile string
	var enable bool
	c := &cobra.Command{
		Use:   "set",
		Short: "Store an integration (credentials encrypted at rest)",
		Long: `Creates or updates an integrations row. Credentials are taken as plain JSON
from --credentials-file and encrypted with QOGNICAL_ENCRYPTION_KEY before
persistence.

Example:

  echo '{"base_url":"https://cloud.example.com","username":"alice",
         "app_password":"...","calendar":"personal"}' > nc.json
  qognical integrations set \
    --owner=<users.id> \
    --provider=nextcloud \
    --credentials-file=nc.json \
    --enable

The unencrypted config field (provider-specific endpoint overrides, calendar
ids, etc.) can be supplied via --config-file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner == "" || provider == "" || credsFile == "" {
				return errors.New("--owner, --provider and --credentials-file are required")
			}
			if !validProvider(provider) {
				return fmt.Errorf("unknown provider %q (msgraph/microsoft/nextcloud/google/jitsi/stripe/paypal)", provider)
			}

			cfg, err := config.LoadStrict()
			if err != nil {
				return fmt.Errorf("load env: %w", err)
			}
			master, err := crypto.NewMaster(cfg.EncryptionKey)
			if err != nil {
				return err
			}

			plain, err := os.ReadFile(credsFile)
			if err != nil {
				return err
			}
			if !json.Valid(plain) {
				return fmt.Errorf("--credentials-file does not contain valid JSON")
			}
			ciphertext, err := master.Encrypt(plain)
			if err != nil {
				return err
			}

			var rawConfig []byte
			if configFile != "" {
				if rawConfig, err = os.ReadFile(configFile); err != nil {
					return err
				}
				if !json.Valid(rawConfig) {
					return fmt.Errorf("--config-file does not contain valid JSON")
				}
			}

			return app.RunInTransaction(func(txApp core.App) error {
				coll, err := txApp.FindCollectionByNameOrId(migrations.CollIntegrations)
				if err != nil {
					return err
				}
				existing, _ := txApp.FindFirstRecordByFilter(migrations.CollIntegrations,
					"owner = {:owner} && provider = {:provider}",
					dbx.Params{"owner": owner, "provider": provider})
				var rec *core.Record
				if existing != nil {
					rec = existing
				} else {
					rec = core.NewRecord(coll)
					rec.Set("owner", owner)
					rec.Set("provider", provider)
				}
				rec.Set("credentials", ciphertext)
				if rawConfig != nil {
					rec.Set("config", json.RawMessage(rawConfig))
				}
				rec.Set("sync_enabled", enable)
				if err := txApp.Save(rec); err != nil {
					return err
				}
				out := map[string]any{
					"id":           rec.Id,
					"provider":     provider,
					"owner":        owner,
					"sync_enabled": enable,
				}
				_ = ctxJSON(context.Background(), out)
				return nil
			})
		},
	}
	c.Flags().StringVar(&owner, "owner", "", "users.id of the host the integration belongs to")
	c.Flags().StringVar(&provider, "provider", "", "msgraph|microsoft|nextcloud|google|jitsi|stripe|paypal")
	c.Flags().StringVar(&credsFile, "credentials-file", "", "JSON file with plaintext credentials (encrypted on the way in)")
	c.Flags().StringVar(&configFile, "config-file", "", "optional JSON file with unencrypted config overrides")
	c.Flags().BoolVar(&enable, "enable", true, "set sync_enabled=true (default true)")
	return c
}

func listCmd(app core.App) *cobra.Command {
	var owner string
	c := &cobra.Command{
		Use:   "list",
		Short: "List configured integrations (credentials remain encrypted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			exprs := []dbx.Expression{}
			if owner != "" {
				exprs = append(exprs, dbx.HashExp{"owner": owner})
			}
			recs, err := app.FindAllRecords(migrations.CollIntegrations, exprs...)
			if err != nil {
				return err
			}
			out := make([]map[string]any, 0, len(recs))
			for _, r := range recs {
				out = append(out, map[string]any{
					"id":           r.Id,
					"owner":        r.GetString("owner"),
					"provider":     r.GetString("provider"),
					"sync_enabled": r.GetBool("sync_enabled"),
					"last_error":   r.GetString("last_error"),
				})
			}
			return ctxJSON(context.Background(), out)
		},
	}
	c.Flags().StringVar(&owner, "owner", "", "filter by users.id")
	return c
}

func removeCmd(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Delete an integration record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := app.FindRecordById(migrations.CollIntegrations, args[0])
			if err != nil {
				return err
			}
			return app.Delete(rec)
		},
	}
}

func ctxJSON(_ context.Context, v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func validProvider(p string) bool {
	switch p {
	case "msgraph", "microsoft", "nextcloud", "google", "jitsi", "stripe", "paypal":
		return true
	}
	return false
}
