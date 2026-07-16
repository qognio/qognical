package api

import (
	"testing"

	"github.com/qognio/qognical/internal/token"
)

// tokenAuthorizes encodes the manage-token policy (2026-07-16):
//   - the pipeline only ever mints ONE invitee token (the manage/view link
//     from the confirmation mail), which must authorize view+cancel+reschedule
//   - action-specific tokens stay exclusive to their action (plus view)
//
// The old switch demanded action-specific tokens that nothing ever issued —
// legitimate cancels only "worked" because the surrounding guard was broken
// (writeErr returns e.JSON's nil, so `if err != nil` never fired and even
// garbage tokens fell through to the cancel). This test pins the fixed policy.
func TestTokenAuthorizes(t *testing.T) {
	cases := []struct {
		granted, requested token.Action
		want               bool
	}{
		// view/manage token = full manage authority
		{token.ActionView, token.ActionView, true},
		{token.ActionView, token.ActionCancel, true},
		{token.ActionView, token.ActionReschedule, true},
		// specific tokens: own action + view, nothing else
		{token.ActionCancel, token.ActionCancel, true},
		{token.ActionCancel, token.ActionView, true},
		{token.ActionCancel, token.ActionReschedule, false},
		{token.ActionReschedule, token.ActionReschedule, true},
		{token.ActionReschedule, token.ActionView, true},
		{token.ActionReschedule, token.ActionCancel, false},
		// unknown requested action is always denied
		{token.ActionView, token.Action("delete"), false},
	}
	for _, c := range cases {
		if got := tokenAuthorizes(c.granted, c.requested); got != c.want {
			t.Errorf("tokenAuthorizes(%q, %q) = %v, want %v", c.granted, c.requested, got, c.want)
		}
	}
}
