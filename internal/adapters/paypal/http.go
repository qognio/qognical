package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
)

// formPostWithHeaders mirrors httpx.DoForm but accepts a header map. PayPal's
// OAuth endpoint needs an explicit Basic-auth header on top of the form body.
func formPostWithHeaders(ctx context.Context, target string, form map[string]string, headers map[string]string, out any) error {
	values := url.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return adapters.ErrAuth
	}
	if resp.StatusCode >= 500 {
		return adapters.ErrUnavailable
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &httpx.APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
