package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// ErrorCode mirrors docs/planning/06.
type ErrorCode string

const (
	CodeInvalidRequest         ErrorCode = "INVALID_REQUEST"
	CodeIntakeValidationFailed ErrorCode = "INTAKE_VALIDATION_FAILED"
	CodeSlotUnavailable        ErrorCode = "SLOT_UNAVAILABLE"
	CodeSlotOutsideAvail       ErrorCode = "SLOT_OUTSIDE_AVAILABILITY"
	CodeSlotTooSoon            ErrorCode = "SLOT_TOO_SOON"
	CodeSlotTooFar             ErrorCode = "SLOT_TOO_FAR"
	CodeEventTypeInactive      ErrorCode = "EVENT_TYPE_INACTIVE"
	CodeTokenInvalid           ErrorCode = "TOKEN_INVALID"
	CodeTokenExpired           ErrorCode = "TOKEN_EXPIRED"
	CodeTokenAlreadyUsed       ErrorCode = "TOKEN_ALREADY_USED"
	CodeRateLimitExceeded      ErrorCode = "RATE_LIMIT_EXCEEDED"
	CodeCaptchaFailed          ErrorCode = "CAPTCHA_FAILED"
	CodePaymentRequired        ErrorCode = "PAYMENT_REQUIRED"
	CodeIntegrationUnavailable ErrorCode = "INTEGRATION_UNAVAILABLE"
	CodeInternalError          ErrorCode = "INTERNAL_ERROR"
	CodeNotFound               ErrorCode = "NOT_FOUND"
)

type apiError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Details   any       `json:"details,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}

type errorBody struct {
	Error apiError `json:"error"`
}

func writeErr(e *core.RequestEvent, status int, code ErrorCode, message string, details any) error {
	return e.JSON(status, errorBody{Error: apiError{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: newRequestID(),
	}})
}

func httpStatusFor(c ErrorCode) int {
	switch c {
	case CodeInvalidRequest, CodeIntakeValidationFailed, CodeCaptchaFailed:
		return http.StatusBadRequest
	case CodeSlotUnavailable:
		return http.StatusConflict
	case CodeSlotOutsideAvail, CodeSlotTooSoon, CodeSlotTooFar:
		return http.StatusUnprocessableEntity
	case CodeEventTypeInactive, CodeTokenAlreadyUsed:
		return http.StatusGone
	case CodeTokenInvalid, CodeTokenExpired:
		return http.StatusUnauthorized
	case CodeRateLimitExceeded:
		return http.StatusTooManyRequests
	case CodePaymentRequired:
		return http.StatusPaymentRequired
	case CodeIntegrationUnavailable:
		return http.StatusServiceUnavailable
	case CodeNotFound:
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}
