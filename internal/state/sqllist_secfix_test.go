package state

import (
	"strings"
	"testing"
)

// ActiveStatusSQLList must render exactly the ActiveStatuses() set, quoted for
// a SQL IN(...). Regression guard for 2026-07-16: several slot/busy filters had
// a hand-typed list that omitted pending_approval, so approval bookings didn't
// hold their slot (doppelbuchung). Building the list from ActiveStatuses()
// makes drift impossible; this test pins the contract.
func TestActiveStatusSQLList(t *testing.T) {
	got := ActiveStatusSQLList()
	want := "'draft','pending_approval','pending_payment','processing','confirmed'"
	if got != want {
		t.Fatalf("ActiveStatusSQLList() = %q, want %q", got, want)
	}
	for _, s := range ActiveStatuses() {
		if !strings.Contains(got, "'"+string(s)+"'") {
			t.Errorf("status %q missing from SQL list %q", s, got)
		}
	}
}
