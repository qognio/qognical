package adapters

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qognio/qognical/internal/crypto"
	"github.com/qognio/qognical/internal/store"
)

// Registry resolves per-host adapter instances by reading the `integrations`
// collection, decrypting credentials, and constructing the right adapter
// implementation. Returning nil for "not configured" is the way callers see
// the optional-integration story — the pipeline simply skips the step.
type Registry struct {
	repo     *store.Repo
	master   *crypto.Master
	calendar map[string]CalendarFactory
	meeting  map[string]MeetingFactory
	payment  map[string]PaymentFactory
}

// Factory signatures: each adapter package registers one. We pass the
// decrypted credentials JSON, the unencrypted config JSON, and the master
// key (so adapters that need additional sub-key derivation — e.g. Jitsi
// JWT signing — can use it).
type CalendarFactory func(creds json.RawMessage, config json.RawMessage) (CalendarProvider, error)
type MeetingFactory func(creds json.RawMessage, config json.RawMessage) (MeetingProvider, error)
type PaymentFactory func(creds json.RawMessage, config json.RawMessage) (PaymentProvider, error)

func NewRegistry(repo *store.Repo, master *crypto.Master) *Registry {
	return &Registry{
		repo: repo, master: master,
		calendar: map[string]CalendarFactory{},
		meeting:  map[string]MeetingFactory{},
		payment:  map[string]PaymentFactory{},
	}
}

func (r *Registry) RegisterCalendar(name string, f CalendarFactory) { r.calendar[name] = f }
func (r *Registry) RegisterMeeting(name string, f MeetingFactory)   { r.meeting[name] = f }
func (r *Registry) RegisterPayment(name string, f PaymentFactory)   { r.payment[name] = f }

// CalendarForHost returns the configured CalendarProvider for hostID, or
// (nil, nil) when no calendar integration exists. (nil, err) signals a real
// problem (decryption fail, malformed JSON).
func (r *Registry) CalendarForHost(hostID string) (CalendarProvider, error) {
	creds, conf, provider, err := r.loadIntegration(hostID, "calendar")
	if err != nil {
		return nil, err
	}
	if provider == "" {
		return nil, nil
	}
	f, ok := r.calendar[provider]
	if !ok {
		return nil, fmt.Errorf("calendar provider %q not registered", provider)
	}
	prov, err := f(creds, conf)
	if err != nil {
		return nil, err
	}
	// Persist rotated OAuth secrets (e.g. Microsoft's refresh_token) so the
	// integration survives rotation and process restarts. Best-effort: a
	// failure only means the older token is reloaded next time.
	if rot, ok := prov.(CredentialRotator); ok {
		rot.SetOnCredentialChange(func(updated json.RawMessage) {
			enc, encErr := r.master.Encrypt(updated)
			if encErr != nil {
				slog.Warn("encrypt rotated credentials failed", "host", hostID, "provider", provider, "err", encErr)
				return
			}
			if upErr := r.repo.UpdateIntegrationCredentials(hostID, provider, enc); upErr != nil {
				slog.Warn("persist rotated credentials failed", "host", hostID, "provider", provider, "err", upErr)
			}
		})
	}
	return prov, nil
}

// MeetingForName builds a MeetingProvider from instance-level config (no
// per-host integration row needed for stateless meeting providers, e.g.
// Jitsi public mode).
func (r *Registry) MeetingForName(name string, conf json.RawMessage) (MeetingProvider, error) {
	f, ok := r.meeting[name]
	if !ok {
		return nil, nil
	}
	return f(nil, conf)
}

// MeetingForHost reads the host's encrypted integrations row for a meeting
// provider (Zoom currently — Jitsi-JWT could also live here later) and
// builds a configured adapter. Returns (nil, nil) when no integration is
// configured for that host+provider.
func (r *Registry) MeetingForHost(hostID, providerName string) (MeetingProvider, error) {
	f, ok := r.meeting[providerName]
	if !ok {
		return nil, nil
	}
	raw, conf, found, err := r.repo.FindIntegrationCredentials(hostID, providerName)
	if err != nil || !found {
		return nil, err
	}
	creds, err := r.master.Decrypt(raw)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s creds: %w", providerName, err)
	}
	return f(creds, conf)
}

// PaymentForName builds a PaymentProvider from instance-level config (Stripe/
// PayPal keys live in env, single set per instance for v1.0).
func (r *Registry) PaymentForName(name string, creds json.RawMessage, conf json.RawMessage) (PaymentProvider, error) {
	f, ok := r.payment[name]
	if !ok {
		return nil, nil
	}
	return f(creds, conf)
}

// loadIntegration walks the `integrations` collection rows for hostID and
// returns the single active integration for the desired family. Calendar is
// the only family currently host-scoped; meeting + payment are instance-scoped.
//
// If a host has MORE than one active integration for the family (e.g. both
// msgraph and microsoft enabled), the choice is ambiguous — silently taking the
// first by list order would let one provider shadow another, so we return a
// visible configuration error instead of guessing which calendar is canonical.
//
// Returns (decryptedCreds, config, providerName, error). (nil,nil,"",nil) when
// no integration is configured.
func (r *Registry) loadIntegration(hostID, family string) (json.RawMessage, json.RawMessage, string, error) {
	families := map[string][]string{
		"calendar": {"msgraph", "microsoft", "nextcloud", "google"},
	}
	candidates, ok := families[family]
	if !ok {
		return nil, nil, "", fmt.Errorf("unknown family %q", family)
	}

	var (
		names   []string
		rawCred string
		conf    json.RawMessage
	)
	for _, name := range candidates {
		raw, c, found, err := r.repo.FindIntegrationCredentials(hostID, name)
		if err != nil {
			return nil, nil, "", err
		}
		if !found {
			continue
		}
		names = append(names, name)
		rawCred, conf = raw, c
	}

	switch len(names) {
	case 0:
		return nil, nil, "", nil
	case 1:
		creds, err := r.master.Decrypt(rawCred)
		if err != nil {
			return nil, nil, "", fmt.Errorf("decrypt %s credentials: %w", names[0], err)
		}
		return creds, conf, names[0], nil
	default:
		return nil, nil, "", fmt.Errorf(
			"host %s has multiple active %s integrations (%s); disable all but one",
			hostID, family, strings.Join(names, ", "))
	}
}
