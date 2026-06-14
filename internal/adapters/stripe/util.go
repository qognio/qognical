package stripe

import (
	"context"
	"io"
	"net/http"
)

// newRequest and readAll exist so stripe.go above doesn't need to import
// "net/http" and "io" itself (keeps the giant file focused on Stripe-specific
// logic). These helpers also enable swapping the transport for tests via
// DefaultClient on httpx.
func newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, body)
}

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
