package connectix

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Classes of failure a caller usually branches on. Test with errors.Is; an
// [*APIError] matches the class its HTTP status belongs to.
var (
	// ErrUnauthorized means the token is wrong, or belongs to the other host
	// (the sandbox token on the production host, or the reverse): HTTP 401.
	ErrUnauthorized = errors.New("connectix: unauthorized")
	// ErrInsufficientFunds means the account balance cannot cover the
	// message: HTTP 402. Top the account up; a retry does not help.
	ErrInsufficientFunds = errors.New("connectix: insufficient funds")
	// ErrRestricted means staff disabled the resource for the application
	// (HTTP 403, "access_restricted"). A retry does not help.
	ErrRestricted = errors.New("connectix: access restricted")
	// ErrNotAcceptable means traffic is suspended, or no sender ID exists for
	// the recipient's country: HTTP 406.
	ErrNotAcceptable = errors.New("connectix: not acceptable")
	// ErrInProgress means a request with the same idempotency key is still
	// being processed: HTTP 409. Wait [APIError.RetryAfter] and repeat the
	// request with the same key; the answer is the stored one.
	ErrInProgress = errors.New("connectix: request in progress")
	// ErrValidation means a field was refused: HTTP 422. [APIError.Raw]
	// maps each field path to its reason. It is also the answer to a repeat
	// of an idempotency key with a different body.
	ErrValidation = errors.New("connectix: validation failed")
	// ErrRateLimited means the application's request budget for the minute is
	// spent: HTTP 429. Wait [APIError.RetryAfter].
	ErrRateLimited = errors.New("connectix: rate limited")
	// ErrNoContract means the company has not signed the contract for the
	// template's channel: HTTP 451. A person signs it in the console; a
	// retry does not help.
	ErrNoContract = errors.New("connectix: no signed contract for the channel")
	// ErrUnprocessable means the request could not be processed: HTTP 502, as
	// for a phone number that cannot be parsed, a country that is not enabled
	// or no provider being available.
	ErrUnprocessable = errors.New("connectix: request could not be processed")
	// ErrTemplateNotFound means the template does not exist or is not
	// approved: an HTTP 400 whose message is "The template was not found."
	ErrTemplateNotFound = errors.New("connectix: template not found")

	// ErrOutcomeUnknown means the request may have reached Connectix and no
	// clear answer came back, so a message may or may not have been sent: a
	// transport error that is not clearly from before sending (a timeout, a
	// dropped connection, an error a custom transport makes up), or a 2xx
	// that could not be read. A failure that cannot have reached Connectix is
	// not this: a refused dial, a DNS failure, a failed TLS handshake or
	// certificate check, or a context done before the call. Repeat a send
	// only with the same idempotency key.
	ErrOutcomeUnknown = errors.New("connectix: outcome unknown")
)

// templateNotFound is the message of the 400 that ErrTemplateNotFound names.
const templateNotFound = "The template was not found."

// maxRetryAfter caps a Retry-After a server states, so a hostile or broken
// value cannot overflow a time.Duration or park a caller for good.
const maxRetryAfter = 24 * time.Hour

// APIError is an error Connectix answered with a non-2xx status.
type APIError struct {
	// Status is the HTTP status.
	Status int
	// Message is Connectix's reason: the plain JSON string it answered with,
	// or the "error" code (else the "message") of an object. It is empty for a
	// body of another shape (a 422's field map is in Raw) and for a body that
	// is not JSON. Error never includes it.
	Message string
	// RetryAfter is how long Connectix asks to wait, from the Retry-After
	// header or the body's retry_after; zero when it asked for nothing.
	RetryAfter time.Duration
	// Raw is the body exactly as received, for a caller that keeps it to
	// diagnose a failure. It is nil when there was no body or the body was
	// not JSON. It can name fields the caller sent: do not log it as it is.
	// Error never includes it.
	Raw json.RawMessage
}

// Error names the status alone. Connectix's reasons can quote what the
// caller sent (a phone number), so they stay in Message and Raw.
func (e *APIError) Error() string { return "connectix: HTTP " + strconv.Itoa(e.Status) }

// Is matches e against the class sentinels, so callers can write
// errors.Is(err, connectix.ErrRateLimited).
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrInsufficientFunds:
		return e.Status == http.StatusPaymentRequired
	case ErrRestricted:
		return e.Status == http.StatusForbidden
	case ErrNotAcceptable:
		return e.Status == http.StatusNotAcceptable
	case ErrInProgress:
		return e.Status == http.StatusConflict
	case ErrValidation:
		return e.Status == http.StatusUnprocessableEntity
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case ErrNoContract:
		return e.Status == http.StatusUnavailableForLegalReasons
	case ErrUnprocessable:
		return e.Status == http.StatusBadGateway
	case ErrTemplateNotFound:
		return e.Status == http.StatusBadRequest && e.Message == templateNotFound
	}
	return false
}

// decodeAPIError reads an error answer. Connectix answers with a plain JSON
// string, an object with "error" (a code) or "message", or a 422's map of
// field path to reason; anything else, such as an HTML page from a proxy,
// leaves Message empty.
func decodeAPIError(raw []byte, status int, header http.Header, now time.Time) *APIError {
	e := &APIError{Status: status}
	var bodyRetry time.Duration
	if json.Valid(raw) {
		e.Raw = bytes.Clone(raw)
		trimmed := bytes.TrimSpace(raw)
		switch {
		case len(trimmed) > 0 && trimmed[0] == '"':
			_ = json.Unmarshal(trimmed, &e.Message)
		case len(trimmed) > 0 && trimmed[0] == '{':
			var fields map[string]json.RawMessage
			if json.Unmarshal(trimmed, &fields) == nil {
				e.Message = firstString(fields, "error", "message")
				bodyRetry = seconds(fields["retry_after"])
			}
		}
	}
	if d, ok := retryAfterHeader(header.Get("Retry-After"), now); ok {
		e.RetryAfter = d
	} else {
		e.RetryAfter = bodyRetry
	}
	return e
}

// firstString returns the first of keys whose value is a JSON string.
func firstString(fields map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(fields[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// seconds reads a JSON whole number of seconds as a duration; anything else,
// or a negative number, is zero.
func seconds(raw json.RawMessage) time.Duration {
	var n int64
	if json.Unmarshal(raw, &n) != nil {
		// A fraction or a string is not a number of seconds.
		return 0
	}
	return clampSeconds(n)
}

func clampSeconds(n int64) time.Duration {
	switch {
	case n <= 0:
		return 0
	case n >= int64(maxRetryAfter/time.Second):
		return maxRetryAfter
	}
	return time.Duration(n) * time.Second
}

// retryAfterHeader reads a Retry-After header: a number of seconds or an
// HTTP date. ok is false when the header is absent or means nothing.
func retryAfterHeader(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return clampSeconds(n), true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0, true
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}
	return 0, false
}
