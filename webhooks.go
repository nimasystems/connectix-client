package connectix

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"
)

// Errors VerifyWebhook returns for a delivery it must not trust. Neither
// carries the secret, the signature or any of the body.
var (
	// ErrBadSignature means the delivery's signature does not match its
	// timestamp and body under the secret, or the signature or the timestamp
	// header is malformed. Answer 401 and do nothing with the body.
	ErrBadSignature = errors.New("connectix: the webhook signature is not valid")
	// ErrStaleTimestamp means the signature is valid but the delivery's
	// timestamp is more than [WebhookTolerance] from now, either way: a
	// delivery that old may be a replay. Answer 4xx and Connectix retries
	// with a fresh timestamp.
	ErrStaleTimestamp = errors.New("connectix: the webhook timestamp is outside the allowed window")
)

// WebhookTolerance is how far a signed delivery's timestamp may be from the
// receiver's clock, in either direction. Connectix's rule is to reject
// timestamps older than 5 minutes; a delivery from the future is refused by
// the same window, so a receiver whose clock runs fast does not widen it.
const WebhookTolerance = 5 * time.Minute

// signaturePrefix names the signature scheme Connectix uses today.
const signaturePrefix = "v1="

// VerifyWebhook checks a signed delivery from a Connectix integration hook.
// timestamp and signature are the values of the X-Connectix-Timestamp and
// X-Connectix-Signature headers, body is the raw request body exactly as it
// arrived (verify before decoding: a re-encoded body signs differently), and
// secret is the 32-byte secret chosen at [Client.RegisterIntegration].
//
// The signature must be "v1=" and the lower-case hex HMAC-SHA256 of
// timestamp + "." + body keyed by secret, compared in constant time, and the
// timestamp must be a Unix time in seconds within [WebhookTolerance] of now.
// It returns [ErrBadSignature] or [ErrStaleTimestamp], and a plain error for
// an empty secret, which is the caller's mistake and would verify anything
// signed with no key. Deliveries to a per-message callback URL are not
// signed; do not pass them here.
//
// The signature is checked first, so a forged delivery learns nothing about
// the window. Verification does not stop replays inside the window: drop
// duplicates on [Callback.EventID].
func VerifyWebhook(secret []byte, timestamp, signature string, body []byte, now time.Time) error {
	if len(secret) == 0 {
		return errors.New("connectix: the webhook secret is empty")
	}
	ts, ok := parseUnixSeconds(timestamp)
	if !ok {
		return ErrBadSignature
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	want := signaturePrefix + hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return ErrBadSignature
	}
	age := now.Unix() - ts
	if age > int64(WebhookTolerance/time.Second) || age < -int64(WebhookTolerance/time.Second) {
		return ErrStaleTimestamp
	}
	return nil
}

// parseUnixSeconds reads a header value that is only decimal digits. It is
// stricter than strconv alone, which also takes a sign.
func parseUnixSeconds(s string) (int64, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Callback is a delivery-status event, the body of a hook of type
// "callback": posted each time a message's status changes.
type Callback struct {
	// EventID is the event's globally unique id, also sent as the
	// X-Connectix-Event-Id header. Delivery is at-least-once: drop repeats
	// on it.
	EventID string
	// ID is the message's id, as [Message.ID].
	ID string
	// Status is the new status, as [Message.Status].
	Status string
	// Channel is "sms", "viber" or "noti".
	Channel string
	// Reference is the reference the message was sent with; empty when it
	// had none.
	Reference string
	// FallbackID is set when the message belongs to a fallback flow.
	FallbackID string
	// CreatedAt is when the status was recorded; the order of events is not
	// guaranteed, so order by this. The zero time if the body had none.
	CreatedAt time.Time
}

// ParseCallback decodes the body of a "callback" hook, once
// [VerifyWebhook] accepted it. It requires id, status and eventId and
// ignores fields it does not know, so a new field does not break a receiver.
// Its errors never quote the body, which can hold a reference of the
// caller's.
func ParseCallback(body []byte) (Callback, error) {
	var wire struct {
		ID         *string   `json:"id"`
		Status     *string   `json:"status"`
		EventID    *string   `json:"eventId"`
		Channel    string    `json:"channel"`
		Reference  string    `json:"reference"`
		FallbackID string    `json:"fallbackId"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&wire); err != nil {
		return Callback{}, errors.New("connectix: the callback body is not the expected JSON: " + describeDecodeError(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Callback{}, errors.New("connectix: the callback body has data after its JSON value")
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"id", wire.ID}, {"status", wire.Status}, {"eventId", wire.EventID}} {
		if f.v == nil || *f.v == "" {
			return Callback{}, errors.New("connectix: the callback body has no " + f.name)
		}
	}
	return Callback{
		EventID: *wire.EventID, ID: *wire.ID, Status: *wire.Status,
		Channel: wire.Channel, Reference: wire.Reference, FallbackID: wire.FallbackID,
		CreatedAt: wire.CreatedAt,
	}, nil
}
