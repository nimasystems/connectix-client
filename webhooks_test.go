package connectix

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

var webhookNow = time.Unix(1_790_000_000, 0)

const webhookSecret = "0123456789abcdef0123456789abcdef"

// sign is the rule from the API document, written out again here so a
// mistake in the library cannot hide in a shared helper.
func sign(secret string, ts int64, body []byte) (timestamp, signature string) {
	timestamp = strconv.FormatInt(ts, 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return timestamp, "v1=" + hex.EncodeToString(m.Sum(nil))
}

const callbackBody = `{"id":"8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12","status":"delivered","channel":"viber","createdAt":"2026-09-20T10:15:41+03:00","eventId":"c1f4b7a2-3d5e-4f60-8a9b-1c2d3e4f5a6b","reference":"order-1234-shipped"}`

func TestAWebhookSignedOverTheRawBodyVerifies(t *testing.T) {
	body := []byte(callbackBody)
	ts, sig := sign(webhookSecret, webhookNow.Unix(), body)
	if err := VerifyWebhook([]byte(webhookSecret), ts, sig, body, webhookNow); err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	// The edges of the window verify too: 5 minutes either way.
	for _, off := range []int64{-300, 300} {
		ts, sig := sign(webhookSecret, webhookNow.Unix()+off, body)
		if err := VerifyWebhook([]byte(webhookSecret), ts, sig, body, webhookNow); err != nil {
			t.Errorf("offset %d s: %v", off, err)
		}
	}
}

func TestAWebhookWhoseBodyChangedByOneByteIsRefused(t *testing.T) {
	body := []byte(callbackBody)
	ts, sig := sign(webhookSecret, webhookNow.Unix(), body)
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-3] ^= 1
	if err := VerifyWebhook([]byte(webhookSecret), ts, sig, tampered, webhookNow); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered body: err = %v, want ErrBadSignature", err)
	}
	if err := VerifyWebhook([]byte("a-different-secret-of-32-bytes!!!"), ts, sig, body, webhookNow); !errors.Is(err, ErrBadSignature) {
		t.Errorf("other secret: err = %v, want ErrBadSignature", err)
	}
	// The timestamp is signed too: moving it by a second breaks the signature.
	if err := VerifyWebhook([]byte(webhookSecret), strconv.FormatInt(webhookNow.Unix()+1, 10), sig, body, webhookNow); !errors.Is(err, ErrBadSignature) {
		t.Errorf("moved timestamp: err = %v, want ErrBadSignature", err)
	}
}

func TestAWebhookTimestampSixMinutesOldOrAheadIsStale(t *testing.T) {
	body := []byte(callbackBody)
	for _, off := range []int64{-360, 360, -301, 301} {
		ts, sig := sign(webhookSecret, webhookNow.Unix()+off, body)
		err := VerifyWebhook([]byte(webhookSecret), ts, sig, body, webhookNow)
		if !errors.Is(err, ErrStaleTimestamp) {
			t.Errorf("offset %d s: err = %v, want ErrStaleTimestamp", off, err)
		}
		if errors.Is(err, ErrBadSignature) {
			t.Errorf("offset %d s: a stale delivery is not a bad signature", off)
		}
	}
}

func TestASignatureWithoutV1OrNotHexIsRefused(t *testing.T) {
	body := []byte(callbackBody)
	ts, sig := sign(webhookSecret, webhookNow.Unix(), body)
	hexPart := strings.TrimPrefix(sig, "v1=")
	tests := map[string]string{
		"empty":             "",
		"no prefix":         hexPart,
		"other version":     "v2=" + hexPart,
		"prefix alone":      "v1=",
		"not hex":           "v1=" + strings.Repeat("zz", 32),
		"too short":         sig[:len(sig)-2],
		"too long":          sig + "00",
		"upper case hex":    "v1=" + strings.ToUpper(hexPart),
		"upper case prefix": "V1=" + hexPart,
		"padded":            " " + sig,
	}
	for name, bad := range tests {
		if err := VerifyWebhook([]byte(webhookSecret), ts, bad, body, webhookNow); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}
}

func TestAWebhookTimestampThatIsNotAUnixIntegerIsRefused(t *testing.T) {
	body := []byte(callbackBody)
	for _, ts := range []string{"", "abc", "1.5", "+1790000000", " 1790000000", "1790000000 ", "99999999999999999999"} {
		_, sig := sign(webhookSecret, webhookNow.Unix(), body)
		if err := VerifyWebhook([]byte(webhookSecret), ts, sig, body, webhookNow); !errors.Is(err, ErrBadSignature) {
			t.Errorf("timestamp %q: err = %v, want ErrBadSignature", ts, err)
		}
	}
}

func TestAnEmptySecretNeverVerifiesAnything(t *testing.T) {
	body := []byte(callbackBody)
	ts, sig := sign("", webhookNow.Unix(), body) // what an unset secret would sign
	err := VerifyWebhook(nil, ts, sig, body, webhookNow)
	if err == nil {
		t.Fatal("an empty secret verified a delivery")
	}
	if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrStaleTimestamp) {
		t.Errorf("err = %v: an empty secret is the caller's mistake, not a bad delivery", err)
	}
}

func TestAVerificationErrorNeverQuotesTheSecretTheSignatureOrTheBody(t *testing.T) {
	body := []byte(callbackBody)
	ts, sig := sign(webhookSecret, webhookNow.Unix(), body)
	for _, err := range []error{
		VerifyWebhook([]byte(webhookSecret), ts, "v1=00", body, webhookNow),
		VerifyWebhook([]byte(webhookSecret), "zz", sig, body, webhookNow),
		VerifyWebhook([]byte(webhookSecret), ts, sig, body, webhookNow.Add(time.Hour)),
	} {
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, leak := range []string{webhookSecret, sig, "order-1234", "delivered"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("error %q carries %q", err, leak)
			}
		}
	}
}

func TestACallbackIsParsedWithEveryField(t *testing.T) {
	body := `{"id":"m1","status":"delivered","channel":"viber","createdAt":"2026-09-20T10:15:41+03:00","eventId":"e1","reference":"order-1","fallbackId":"f1","extra":"ignored"}`
	got, err := ParseCallback([]byte(body))
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	want := Callback{EventID: "e1", ID: "m1", Status: "delivered", Channel: "viber", Reference: "order-1", FallbackID: "f1"}
	wantAt := time.Date(2026, 9, 20, 7, 15, 41, 0, time.UTC)
	if !got.CreatedAt.Equal(wantAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, wantAt)
	}
	got.CreatedAt = time.Time{}
	if got != want {
		t.Errorf("callback = %+v, want %+v", got, want)
	}
}

func TestACallbackWithoutOptionalFieldsIsParsed(t *testing.T) {
	got, err := ParseCallback([]byte(`{"id":"m1","status":"sent","eventId":"e1"}`))
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if got.ID != "m1" || got.Status != "sent" || got.EventID != "e1" || got.Reference != "" || got.FallbackID != "" || !got.CreatedAt.IsZero() {
		t.Errorf("callback = %+v", got)
	}
}

func TestACallbackWithoutEventIdIsRefused(t *testing.T) {
	tests := map[string]string{
		"no eventId":     `{"id":"m1","status":"sent"}`,
		"empty eventId":  `{"id":"m1","status":"sent","eventId":""}`,
		"no id":          `{"status":"sent","eventId":"e1"}`,
		"no status":      `{"id":"m1","eventId":"e1"}`,
		"null id":        `{"id":null,"status":"sent","eventId":"e1"}`,
		"not JSON":       `<html>502</html>`,
		"empty":          ``,
		"an array":       `[]`,
		"wrong type":     `{"id":5,"status":"sent","eventId":"e1"}`,
		"a bad time":     `{"id":"m1","status":"sent","eventId":"e1","createdAt":"yesterday"}`,
		"trailing value": `{"id":"m1","status":"sent","eventId":"e1"} {}`,
	}
	for name, body := range tests {
		_, err := ParseCallback([]byte(body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "m1") || strings.Contains(err.Error(), "yesterday") || strings.Contains(err.Error(), "502") {
			t.Errorf("%s: error %q quotes the body", name, err)
		}
	}
}
