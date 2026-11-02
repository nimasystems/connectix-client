package connectix

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const acceptedBody = `{"id":"8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12","type":"transactional","phone":"+359881234567","message":{"text":"HuboGO: 123456"},"price":"0.0550","parts":1,"channel":"sms","status":"received","ttl":600,"callbackUrl":null,"sender":"HuboGO","createdAt":"2026-11-02T10:15:30+02:00","reference":"r1"}`

func TestASendPostsTheTemplateWithTheIdempotencyKeyAndTheIntegration(t *testing.T) {
	var method, path, query, body string
	var header http.Header
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, query, header = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(acceptedBody))
	})
	msg, err := c.SendMessage(t.Context(), SendMessageRequest{
		Phone:       "+359881234567",
		Template:    "t1",
		Parameters:  map[string]string{"minutes": "10", "code": "123456"},
		TTL:         600,
		Reference:   "r1",
		Integration: "itok",
	}, "k1")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if method != http.MethodPost || path != "/messages" || query != "integration=itok" {
		t.Errorf("request = %s %s?%s, want POST /messages?integration=itok", method, path, query)
	}
	if got := header.Get("Idempotency-Key"); got != "k1" {
		t.Errorf("Idempotency-Key = %q, want k1", got)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := header.Get("Authorization"); got != testToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	const want = `{"phone":"+359881234567","template":"t1","parameters":{"code":"123456","minutes":"10"},"ttl":600,"reference":"r1"}`
	if body != want {
		t.Errorf("body = %s\nwant   %s", body, want)
	}
	if msg.ID != "8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12" || msg.Status != "received" || msg.Parts != 1 {
		t.Errorf("message = %+v", msg)
	}
	if msg.Channel != "sms" || msg.Type != "transactional" || msg.Price != "0.0550" || msg.Reference != "r1" {
		t.Errorf("message = %+v", msg)
	}
	if want := time.Date(2026, 11, 2, 8, 15, 30, 0, time.UTC); !msg.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", msg.CreatedAt, want)
	}
	if !msg.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt = %v, want zero (the send answer has none)", msg.UpdatedAt)
	}
	if msg.Replayed {
		t.Error("Replayed on a first answer")
	}
	if string(msg.Raw) != acceptedBody {
		t.Errorf("Raw = %s, want the body served", msg.Raw)
	}
}

func TestASendOmitsEmptyFieldsAndAnEmptyKeyAndIntegration(t *testing.T) {
	var query, body string
	var header http.Header
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query, header = r.URL.RawQuery, r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(acceptedBody))
	})
	if _, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, ""); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if body != `{"phone":"+359881234567","template":"t1"}` {
		t.Errorf("body = %s", body)
	}
	if query != "" {
		t.Errorf("query = %q, want none", query)
	}
	if _, ok := header["Idempotency-Key"]; ok {
		t.Error("an empty key was sent as a header")
	}
}

func TestAnIdempotencyKeyThatCannotBeAHeaderIsRefusedBeforeSending(t *testing.T) {
	hits := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	for name, key := range map[string]string{
		"a newline":    "k1\r\nX-Evil: 1",
		"a non-ASCII":  "ключ",
		"too long":     strings.Repeat("k", 129),
		"a DEL":        "k\x7f",
		"a lone space": "k 1",
	} {
		_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, key)
		if err == nil {
			t.Errorf("%s: SendMessage succeeded", name)
			continue
		}
		if errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("%s: a refusal before sending is outcome unknown", name)
		}
		if strings.Contains(err.Error(), "X-Evil") {
			t.Errorf("%s: the error echoes the key: %v", name, err)
		}
	}
	if hits != 0 {
		t.Errorf("hits = %d, want 0", hits)
	}
}

func TestAReplayedAnswerIsMarkedReplayed(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   bool
	}{{"true", true}, {"false", false}, {"", false}} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if tt.header != "" {
				w.Header().Set("Idempotent-Replayed", tt.header)
			}
			_, _ = w.Write([]byte(acceptedBody))
		})
		msg, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if msg.Replayed != tt.want {
			t.Errorf("Idempotent-Replayed %q: Replayed = %v, want %v", tt.header, msg.Replayed, tt.want)
		}
	}
}

func TestTheRateLimitRemainingIsReadAndMinusOneWhenAbsent(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   int
	}{{"57", 57}, {"0", 0}, {"", -1}, {"many", -1}, {"-3", -1}} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if tt.header != "" {
				w.Header().Set("X-RateLimit-Remaining", tt.header)
			}
			_, _ = w.Write([]byte(acceptedBody))
		})
		msg, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if msg.RateLimitRemaining != tt.want {
			t.Errorf("X-RateLimit-Remaining %q: RateLimitRemaining = %d, want %d", tt.header, msg.RateLimitRemaining, tt.want)
		}
	}
}

func TestAGetReadsTheMessageWithItsUpdateTime(t *testing.T) {
	const body = `{"id":"8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12","type":"transactional","phone":"+359881234567","message":{"text":"x"},"price":"0.0000","parts":2,"channel":"sms","status":"delivered","ttl":600,"callbackUrl":null,"reference":null,"createdAt":"2026-11-02T10:15:30+02:00","updatedAt":"2026-11-02T10:15:41+02:00"}`
	var method, path, ctype string
	var hasBody bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		hasBody = len(b) > 0
		w.Header().Set("X-RateLimit-Remaining", "41")
		_, _ = w.Write([]byte(body))
	})
	msg, err := c.GetMessage(t.Context(), "8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if method != http.MethodGet || path != "/messages/8b2f1c0e-5d3a-4e7b-9f21-0c6a7d4e5b12" || ctype != "" || hasBody {
		t.Errorf("request = %s %s (Content-Type %q, body %v)", method, path, ctype, hasBody)
	}
	if msg.Status != "delivered" || msg.Parts != 2 || msg.Reference != "" || msg.RateLimitRemaining != 41 {
		t.Errorf("message = %+v", msg)
	}
	if want := time.Date(2026, 11, 2, 8, 15, 41, 0, time.UTC); !msg.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", msg.UpdatedAt, want)
	}
	if string(msg.Raw) != body {
		t.Errorf("Raw = %s", msg.Raw)
	}
	var generic map[string]any
	if err := json.Unmarshal(msg.Raw, &generic); err != nil || generic["status"] != "delivered" {
		t.Errorf("Raw does not decode: %v", err)
	}
}

func TestAGetRefusesAnIDThatCouldChangeThePath(t *testing.T) {
	hits := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	for _, id := range []string{"", "../me", "a/b", "a?b=c", "a b", "%2e%2e", strings.Repeat("a", 65)} {
		if _, err := c.GetMessage(t.Context(), id); err == nil {
			t.Errorf("GetMessage(%q) succeeded", id)
		}
	}
	if hits != 0 {
		t.Errorf("hits = %d, want 0", hits)
	}
}

func TestAMissingMessageIsAnAPIErrorWithItsReason(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`"Message not found."`))
	})
	_, err := c.GetMessage(t.Context(), "m1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Message != "Message not found." {
		t.Fatalf("err = %v, want an APIError 404 with its message", err)
	}
}
