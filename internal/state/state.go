// Package state enforces the booking state machine from docs/planning/04.
// Direct DB mutation that bypasses Transition is a bug.
package state

import "fmt"

// Status mirrors the bookings.status field values.
type Status string

const (
	StatusDraft           Status = "draft"
	StatusPendingApproval Status = "pending_approval" // v1.1 approval-workflow
	StatusPendingPayment  Status = "pending_payment"
	StatusProcessing      Status = "processing"
	StatusConfirmed       Status = "confirmed"
	StatusCancelled       Status = "cancelled"
	StatusRescheduled     Status = "rescheduled"
	StatusRefunded        Status = "refunded"
	StatusAbandoned       Status = "abandoned"
)

// allowed[from] = set of permitted next states. Encodes the table in Doc 04.
var allowed = map[Status]map[Status]bool{
	"": { // initial create
		StatusDraft:           true,
		StatusPendingApproval: true, // requires_approval=true path
		StatusConfirmed:       true, // unpaid happy path skips draft if caller wants
	},
	StatusDraft: {
		StatusPendingApproval: true,
		StatusPendingPayment:  true,
		StatusConfirmed:       true,
		StatusAbandoned:       true,
	},
	StatusPendingApproval: {
		// host approves → flows into pending_payment (paid) or confirmed (free)
		StatusPendingPayment: true,
		StatusConfirmed:      true,
		StatusCancelled:      true, // host declined or invitee withdrew
		StatusAbandoned:      true, // cron timeout on stale approvals
	},
	StatusPendingPayment: {
		StatusProcessing: true,
		// Stripe's checkout.session.completed jumps us straight from
		// pending_payment to confirmed without an explicit "user on
		// Stripe's page" tick — allow that. Processing remains a valid
		// intermediate for SEPA/async payments.
		StatusConfirmed: true,
		StatusAbandoned: true,
	},
	StatusProcessing: {
		StatusConfirmed: true,
		StatusAbandoned: true,
	},
	StatusConfirmed: {
		StatusCancelled:   true,
		StatusRescheduled: true,
		StatusRefunded:    true,
	},
	StatusCancelled: {
		StatusRefunded: true,
	},
	// terminal: rescheduled, refunded, abandoned
}

// CanTransition reports whether from → to is a legal transition.
func CanTransition(from, to Status) bool {
	return allowed[from][to]
}

// Transition is CanTransition with a typed error for the API layer.
func Transition(from, to Status) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal transition %q → %q", from, to)
	}
	return nil
}

// ActiveStatuses returns the statuses that occupy a slot for the purpose of
// the doppelbuchung check (INV-1). A booking in one of these states blocks
// overlapping bookings. pending_approval is included because the slot is
// effectively held while the host decides.
func ActiveStatuses() []Status {
	return []Status{
		StatusDraft, StatusPendingApproval, StatusPendingPayment,
		StatusProcessing, StatusConfirmed,
	}
}

// ActiveStatusSQLList renders ActiveStatuses() as a SQL IN(...) value list,
// e.g. "'draft','pending_approval',...". Slot/busy/capacity queries MUST use
// this instead of a hand-typed string so the set can never drift from
// ActiveStatuses() again (2026-07-16: several filters silently omitted
// pending_approval → approval bookings didn't hold their slot → doppelbuchung).
func ActiveStatusSQLList() string {
	out := ""
	for i, s := range ActiveStatuses() {
		if i > 0 {
			out += ","
		}
		out += "'" + string(s) + "'"
	}
	return out
}

// IsTerminal returns true if no further transitions are possible.
func IsTerminal(s Status) bool {
	switch s {
	case StatusRescheduled, StatusRefunded, StatusAbandoned:
		return true
	}
	return false
}
