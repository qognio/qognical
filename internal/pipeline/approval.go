package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/qognio/qognical/internal/notifier"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/internal/token"
)

// notifyApprovalRequest emails the host with an approve/decline link. The
// invitee gets a "pending host approval" confirmation in parallel.
func (p *Pipeline) notifyApprovalRequest(b store.Booking, et store.EventType, host store.Host, approvalTok token.Token) error {
	approveURL := fmt.Sprintf("%s/api/public/v1/bookings/%s/approve?token=%s",
		p.baseURL, b.ID, approvalTok.String)
	declineURL := fmt.Sprintf("%s/api/public/v1/bookings/%s/decline?token=%s",
		p.baseURL, b.ID, approvalTok.String)

	// Host notification with action links.
	hostMail := notifier.Booking{
		ID: b.ID, EventTypeTitle: et.Title,
		HostName: host.Name, HostEmail: host.Email,
		InviteeName: b.InviteeName, InviteeEmail: b.InviteeEmail,
		StartUTC: b.StartUTC, EndUTC: b.EndUTC,
		InviteeTZ: b.InviteeTimezone,
		ManageURL: approveURL, // overload: host email's primary CTA = approve
		BaseURL:   p.baseURL,
	}
	if err := p.notifier.SendApprovalRequest(hostMail, declineURL); err != nil {
		return err
	}
	// Invitee confirmation: "request received, pending host approval".
	_ = p.notifier.SendApprovalPending(notifier.Booking{
		ID: b.ID, EventTypeTitle: et.Title,
		HostName: host.Name, HostEmail: host.Email,
		InviteeName: b.InviteeName, InviteeEmail: b.InviteeEmail,
		StartUTC: b.StartUTC, EndUTC: b.EndUTC,
		InviteeTZ: b.InviteeTimezone,
		BaseURL:   p.baseURL,
	})
	return nil
}

// HandleApproval is invoked by the public-API handler when the host clicks
// the approve-link. It transitions the booking and runs whatever tail makes
// sense (payment-start for paid event-types; confirm-tail otherwise).
func (p *Pipeline) HandleApproval(bookingID string) (store.Booking, error) {
	b, err := p.repo.SetBookingApproval(bookingID, "")
	if err != nil {
		return store.Booking{}, err
	}
	et, err := p.repo.FindEventTypeByID(b.EventTypeID)
	if err != nil {
		return b, err
	}
	host, err := p.repo.FindHostByID(b.HostID)
	if err != nil {
		return b, err
	}
	// Issue a fresh manage-token for the invitee (their approval-flow token
	// was single-use and already invalidated).
	tok, _ := p.tokens.Issue(b.ID, token.ActionView, 0)
	_ = p.repo.SetCancelTokenHash(b.ID, tok.Hash)

	if et.PaymentMode != "" && et.PaymentMode != "none" {
		// Paid path: send invitee a checkout link instead of running confirm-tail.
		checkout, err := p.startCheckout(b, et, tok)
		if err == nil {
			_, _ = p.repo.SetBookingPaymentResult(b.ID, state.StatusPendingPayment, "pending", checkout.ExternalID, 0)
			_ = p.notifier.SendApprovalCheckout(notifier.Booking{
				ID: b.ID, EventTypeTitle: et.Title,
				HostName: host.Name, HostEmail: host.Email,
				InviteeName: b.InviteeName, InviteeEmail: b.InviteeEmail,
				StartUTC: b.StartUTC, EndUTC: b.EndUTC,
				InviteeTZ: b.InviteeTimezone, ManageURL: checkout.RedirectURL,
				BaseURL: p.baseURL,
			})
		}
	} else {
		if err := p.confirmTail(b, et, host, tok); err != nil {
			return b, err
		}
	}
	return b, nil
}

// HandleDecline transitions the booking to cancelled and notifies the invitee.
func (p *Pipeline) HandleDecline(bookingID, reason string) (store.Booking, error) {
	b, err := p.repo.SetBookingDeclined(bookingID, reason)
	if err != nil {
		return store.Booking{}, err
	}
	et, _ := p.repo.FindEventTypeByID(b.EventTypeID)
	host, _ := p.repo.FindHostByID(b.HostID)
	_ = p.notifier.SendApprovalDeclined(notifier.Booking{
		ID: b.ID, EventTypeTitle: et.Title,
		HostName: host.Name, HostEmail: host.Email,
		InviteeName: b.InviteeName, InviteeEmail: b.InviteeEmail,
		StartUTC: b.StartUTC, EndUTC: b.EndUTC,
		InviteeTZ: b.InviteeTimezone, BaseURL: p.baseURL,
	}, reason)
	return b, nil
}

// unused, kept for future v1.2 hook
var _ = context.Background
var _ = time.Now
