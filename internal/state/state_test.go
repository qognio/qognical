package state

import "testing"

func TestHappyPathUnpaid(t *testing.T) {
	if err := Transition("", StatusConfirmed); err != nil {
		t.Error(err)
	}
	if err := Transition(StatusConfirmed, StatusCancelled); err != nil {
		t.Error(err)
	}
}

func TestHappyPathPaid(t *testing.T) {
	for _, step := range [][2]Status{
		{"", StatusDraft},
		{StatusDraft, StatusPendingPayment},
		{StatusPendingPayment, StatusProcessing},
		{StatusProcessing, StatusConfirmed},
		{StatusConfirmed, StatusRescheduled},
	} {
		if err := Transition(step[0], step[1]); err != nil {
			t.Errorf("%v: %v", step, err)
		}
	}
}

func TestForbidden(t *testing.T) {
	for _, step := range [][2]Status{
		{StatusConfirmed, StatusDraft},
		{StatusCancelled, StatusConfirmed},
		{StatusAbandoned, StatusConfirmed},
		{StatusRescheduled, StatusCancelled},
	} {
		if err := Transition(step[0], step[1]); err == nil {
			t.Errorf("expected error for %v", step)
		}
	}
}
