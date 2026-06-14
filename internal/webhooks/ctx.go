package webhooks

import (
	"context"
	"time"
)

// contextBg returns a background context with a generous timeout for async
// dispatch tasks. Kept in its own file so test variants can swap it out.
func contextBg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}
