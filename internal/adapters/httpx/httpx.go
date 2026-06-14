// Package httpx is the tiny shared HTTP helper used by every API-talking
// adapter. Keeping it here avoids each adapter re-implementing the same
// boilerplate (timeouts, JSON decode, 4xx vs 5xx triage to ErrAuth /
// ErrUnavailable).
//
// We deliberately do NOT pull in any HTTP-client library — net/http with
// a sensible Client config is enough.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qognio/qognical/internal/adapters"
)

// DefaultClient has reasonable timeouts for provider calls.
var DefaultClient = &http.Client{
	Timeout: 20 * time.Second,
}

// DoJSON marshals body to JSON, sends the request with optional headers,
// and decodes the response into out (may be nil for void calls).
// Returns ErrAuth on 401/403 and ErrUnavailable on 5xx / network errors so
// the caller can map them onto pipeline outcomes per Doc 09 INT-1/INT-2.
func DoJSON(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %d %s", adapters.ErrAuth, resp.StatusCode, string(raw))
	}
	if resp.StatusCode >= 500 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %d %s", adapters.ErrUnavailable, resp.StatusCode, string(raw))
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil || resp.StatusCode == 204 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// DoForm is the form-encoded variant used by OAuth token endpoints.
func DoForm(ctx context.Context, url string, form map[string]string, out any) error {
	var buf bytes.Buffer
	first := true
	for k, v := range form {
		if !first {
			buf.WriteByte('&')
		}
		buf.WriteString(urlencode(k))
		buf.WriteByte('=')
		buf.WriteString(urlencode(v))
		first = false
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %s", adapters.ErrAuth, raw)
	}
	if resp.StatusCode >= 500 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %d %s", adapters.ErrUnavailable, resp.StatusCode, raw)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// APIError carries a 4xx response so the caller can decide on a per-call
// basis whether it's a domain failure or programmer error.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("api %d: %s", e.Status, e.Body) }

// IsNotFound is convenience for adapters whose Delete should be idempotent.
func IsNotFound(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 404 || ae.Status == 410
	}
	return false
}

// urlencode is the tiny subset of url.QueryEscape we need (no fancy reserved
// handling — only alphanum + a few safe chars passthrough, everything else
// becomes percent-encoded). Pulled inline to avoid the import in adapter
// packages.
func urlencode(s string) string {
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
