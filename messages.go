package connectix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// maxIdempotencyKey is the longest Idempotency-Key Connectix accepts.
const maxIdempotencyKey = 128

// maxMessageID bounds a message id (a UUID is 36 characters).
const maxMessageID = 64

// SendMessageRequest is one message from an approved template to one
// recipient. Empty fields are left out of the request.
type SendMessageRequest struct {
	// Phone is the recipient's mobile number in E.164 form
	// (+359881234567). It is personal data: do not log it.
	Phone string `json:"phone,omitempty"`
	// Template is the id of an approved template from [Client.ListTemplates].
	// The template decides the channel and the message type.
	Template string `json:"template,omitempty"`
	// Parameters fills the template's {{ slot }} variables (see
	// [Template.Slots]). A value can be personal data or a secret, such as a
	// one-time code: do not log it.
	Parameters map[string]string `json:"parameters,omitempty"`
	// TTL is how many seconds the provider may keep trying to deliver the
	// message, 60 to 86400; zero lets Connectix pick (43200 for SMS).
	TTL int `json:"ttl,omitempty"`
	// Reference is the caller's own tag for the message (an order or event
	// id, up to 128 characters of A-Z a-z 0-9 . _ : / -), echoed in the answer
	// and in the webhooks.
	Reference string `json:"reference,omitempty"`
	// Integration is the integration token that attributes the message to a
	// registered integration, so its webhooks receive the statuses. It goes in
	// the query, as Connectix has it, and is never put into an error.
	Integration string `json:"-"`
}

// Message is a message as Connectix describes it, from [Client.SendMessage]
// or [Client.GetMessage]. Only the fields a caller branches on are decoded;
// Raw holds the rest.
type Message struct {
	// ID is the message's id, the same as in the webhooks.
	ID string `json:"id"`
	// Status is the lifecycle state: "received" when just accepted, then
	// "processing", "sent", "delivered" or "seen", or a failure such as
	// "undelivered", "failed" or "could_not_be_sent".
	Status string `json:"status"`
	// Channel is "sms", "viber" or "noti".
	Channel string `json:"channel"`
	// Type is "transactional" or "promotional".
	Type string `json:"type"`
	// Price is the charge as a decimal string with four decimals, in EUR;
	// always "0.0000" on the sandbox. It stays a string so no float ever
	// holds money.
	Price string `json:"price"`
	// Reference is the caller's reference, empty when there was none.
	Reference string `json:"reference"`
	// Parts is the number of SMS parts (1 for Viber and Noti).
	Parts int `json:"parts"`
	// CreatedAt is when Connectix accepted the message.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the message last changed. Only [Client.GetMessage]
	// answers it; the zero time otherwise.
	UpdatedAt time.Time `json:"updatedAt"`

	// Replayed is true when Connectix answered the idempotency key from its
	// stored answer: the call sent nothing a second time.
	Replayed bool `json:"-"`
	// RateLimitRemaining is the requests left in the current minute for the
	// application, from X-RateLimit-Remaining; -1 when the header is absent.
	RateLimitRemaining int `json:"-"`
	// Raw is the answer exactly as received, with the recipient and the
	// message text. It is personal data: do not log it as it is.
	Raw json.RawMessage `json:"-"`
}

// SendMessage sends one message from a template. A non-empty idempotencyKey
// (a fresh UUID per logical send, up to 128 printable ASCII characters
// without spaces) is sent as the Idempotency-Key header: a repeat with the
// same key returns the stored answer with [Message.Replayed] set and sends
// nothing again. Without a key, a call that ends in [ErrOutcomeUnknown] must
// not be repeated blindly.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest, idempotencyKey string) (Message, error) {
	if err := validIdempotencyKey(idempotencyKey); err != nil {
		return Message{}, err
	}
	r := request{
		method:         http.MethodPost,
		path:           "messages",
		idempotencyKey: idempotencyKey,
		body:           req,
	}
	if req.Integration != "" {
		r.query = url.Values{"integration": {req.Integration}}
	}
	return c.message(ctx, r)
}

// GetMessage reads a message of the application by the id [Client.SendMessage]
// answered, with its current status.
func (c *Client) GetMessage(ctx context.Context, id string) (Message, error) {
	if err := validMessageID(id); err != nil {
		return Message{}, err
	}
	return c.message(ctx, request{method: http.MethodGet, path: "messages/" + id})
}

func (c *Client) message(ctx context.Context, r request) (Message, error) {
	var m Message
	a, err := c.call(ctx, r, &m)
	if err != nil {
		return Message{}, err
	}
	if m.ID == "" {
		return Message{}, &unexpectedError{reason: "the answer has no id"}
	}
	m.Replayed = a.header.Get("Idempotent-Replayed") == "true"
	m.RateLimitRemaining = -1
	if n, err := strconv.Atoi(a.header.Get("X-RateLimit-Remaining")); err == nil && n >= 0 {
		m.RateLimitRemaining = n
	}
	m.Raw = a.raw
	return m, nil
}

// validIdempotencyKey refuses a key that could not be a header value, before
// anything is sent: net/http would fail the call, and the failure would look
// like a transport error. The error never echoes the key.
func validIdempotencyKey(key string) error {
	if len(key) > maxIdempotencyKey {
		return errors.New("connectix: the idempotency key is longer than " + strconv.Itoa(maxIdempotencyKey) + " characters")
	}
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] >= 0x7f {
			return errors.New("connectix: the idempotency key must be printable ASCII without spaces")
		}
	}
	return nil
}

// validMessageID refuses an id that could change the request path, such as
// "../me" or "a/b", before anything is sent. The error never echoes the id.
func validMessageID(id string) error {
	if id == "" || len(id) > maxMessageID {
		return errors.New("connectix: the message id is empty or too long")
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '-') {
			return errors.New("connectix: the message id may hold only letters, digits and hyphens")
		}
	}
	return nil
}
