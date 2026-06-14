package pipeline

import (
	"sort"
	"time"

	"github.com/qognio/qognical/internal/store"
)

// pickHost selects a host from the event-type's pool per the configured
// strategy. For single-host event-types it returns OwnerID immediately.
// On failure (no candidate free) we fall back to the owner.
func (p *Pipeline) pickHost(et store.EventType, startUTC, endUTC time.Time) (string, error) {
	hosts := et.AllHosts()
	if len(hosts) <= 1 {
		return et.OwnerID, nil
	}
	strategy := et.AssignmentStrategy
	if strategy == "" || strategy == "single" {
		return et.OwnerID, nil
	}

	// First filter: only hosts who don't already have an overlapping booking.
	free := []string{}
	for _, h := range hosts {
		busy, err := p.repo.ActiveBusyForHost(h, startUTC, endUTC)
		if err != nil {
			return "", err
		}
		conflict := false
		for _, b := range busy {
			if b.Start.Before(endUTC) && startUTC.Before(b.End) {
				conflict = true
				break
			}
		}
		if !conflict {
			free = append(free, h)
		}
	}
	if len(free) == 0 {
		// Slot taken everywhere — caller's reservation will fail with
		// SLOT_UNAVAILABLE; pick owner so the error is consistent.
		return et.OwnerID, nil
	}

	switch strategy {
	case "round_robin", "least_loaded":
		// Look at last 30 days of bookings per host and pick the lightest.
		windowFrom := startUTC.Add(-30 * 24 * time.Hour)
		windowTo := startUTC.Add(365 * 24 * time.Hour)
		load, err := p.repo.HostLoadInWindow(free, windowFrom, windowTo)
		if err != nil {
			return free[0], nil
		}
		sort.SliceStable(free, func(i, j int) bool {
			return load[free[i]] < load[free[j]]
		})
		return free[0], nil
	case "collective":
		// v1.1: pick owner; v1.2 should allow tagging all hosts as
		// co-attendees on the calendar event.
		for _, h := range hosts {
			for _, f := range free {
				if h == f {
					return h, nil
				}
			}
		}
		return et.OwnerID, nil
	}
	return et.OwnerID, nil
}
