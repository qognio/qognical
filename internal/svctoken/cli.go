package svctoken

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"

	"github.com/qognio/qognical/internal/store"
)

// Cmd returns the `qognical service-token` command group for the binary's
// root cobra command. Useful for ops scripts: create, list, revoke without
// going through the HTTP API.
func Cmd(app core.App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "service-token",
		Short: "Manage service tokens (ADR-0002)",
	}
	parent.AddCommand(createCmd(app), listCmd(app), revokeCmd(app))
	return parent
}

func createCmd(app core.App) *cobra.Command {
	var name, scopes, host, allowlist, expires string
	c := &cobra.Command{
		Use:   "create",
		Short: "Issue a new service token (raw value shown once)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || scopes == "" {
				return fmt.Errorf("--name and --scopes are required")
			}
			raw, hash, err := Generate()
			if err != nil {
				return err
			}
			scopeList := splitCSV(scopes)
			allowlistList := splitCSV(allowlist)
			var expiry time.Time
			if expires != "" {
				expiry, err = time.Parse(time.RFC3339, expires)
				if err != nil {
					return fmt.Errorf("--expires must be RFC3339: %w", err)
				}
			}
			repo := store.New(app)
			created, err := repo.CreateServiceToken(name, hash, "cli", scopeList, host, allowlistList, expiry)
			if err != nil {
				return err
			}
			out := map[string]any{
				"id":     created.ID,
				"name":   created.Name,
				"token":  raw,
				"scopes": created.Scopes,
			}
			if host != "" {
				out["host_binding"] = host
			}
			if len(allowlistList) > 0 {
				out["event_type_allowlist"] = allowlistList
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	c.Flags().StringVar(&name, "name", "", "human-readable label, e.g. voice-bot or ci-runner")
	c.Flags().StringVar(&scopes, "scopes", "", "comma-separated scopes, e.g. bookings:create,availability:read")
	c.Flags().StringVar(&host, "host", "", "optional host_binding (users.id)")
	c.Flags().StringVar(&allowlist, "event-types", "", "optional comma-separated allowlist of event_type ids")
	c.Flags().StringVar(&expires, "expires", "", "optional RFC3339 expiry timestamp")
	return c
}

func listCmd(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List service tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := store.New(app)
			tokens, err := repo.ListServiceTokens()
			if err != nil {
				return err
			}
			fmt.Printf("%-22s  %-25s  %-30s  %s\n", "ID", "NAME", "SCOPES", "STATUS")
			for _, t := range tokens {
				status := "active"
				if !t.RevokedAt.IsZero() {
					status = "revoked"
				} else if !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt) {
					status = "expired"
				}
				fmt.Printf("%-22s  %-25s  %-30s  %s\n",
					t.ID, t.Name,
					strings.Join(t.Scopes, ","),
					status)
			}
			return nil
		},
	}
}

func revokeCmd(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a service token by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(app).RevokeServiceToken(args[0])
		},
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
