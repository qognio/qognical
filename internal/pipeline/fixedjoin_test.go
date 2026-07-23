package pipeline

import (
	"testing"

	"github.com/pocketbase/pocketbase/tools/types"
)

// fixedJoinURL drives the demo/webinar mode: a valid https link short-circuits
// meeting creation, anything else falls back to the normal auto-create flow.
func TestFixedJoinURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want string
	}{
		{"valid teams link", `{"fixed_join_url":"https://teams.microsoft.com/l/meetup-join/xyz"}`, "https://teams.microsoft.com/l/meetup-join/xyz"},
		{"trims whitespace", `{"fixed_join_url":"  https://teams.microsoft.com/x  "}`, "https://teams.microsoft.com/x"},
		{"empty config", ``, ""},
		{"empty object", `{}`, ""},
		{"empty value", `{"fixed_join_url":""}`, ""},
		{"non-https rejected", `{"fixed_join_url":"http://insecure/x"}`, ""},
		{"junk rejected", `{"fixed_join_url":"javascript:alert(1)"}`, ""},
		{"malformed json", `{not json`, ""},
		{"other keys ignored", `{"jitsi_room":"abc"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fixedJoinURL(types.JSONRaw(c.cfg))
			if got != c.want {
				t.Errorf("fixedJoinURL(%q) = %q, want %q", c.cfg, got, c.want)
			}
		})
	}
}
