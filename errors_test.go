package connectix

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func isErr(err, target error) bool { return errors.Is(err, target) }

func sendOnce(t *testing.T, c *Client) error {
	t.Helper()
	_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	return err
}

func TestEachStatusMapsToItsSentinel(t *testing.T) {
	sentinels := []error{ErrUnauthorized, ErrInsufficientFunds, ErrRestricted, ErrNotAcceptable, ErrInProgress,
		ErrValidation, ErrRateLimited, ErrNoContract, ErrUnprocessable, ErrTemplateNotFound}
	tests := []struct {
		name      string
		status    int
		body      string
		header    map[string]string
		want      error
		wantRetry time.Duration
		wantMsg   string
		hidden    []string // must never appear in Error()
	}{
		{name: "400 template not found", status: 400, body: `"The template was not found."`, want: ErrTemplateNotFound, wantMsg: "The template was not found."},
		{name: "401", status: 401, body: `"This action needs a valid token!"`, want: ErrUnauthorized, wantMsg: "This action needs a valid token!"},
		{name: "402", status: 402, body: `"Insufficient funds."`, want: ErrInsufficientFunds, wantMsg: "Insufficient funds."},
		{name: "403 access restricted", status: 403, body: `{"error":"access_restricted","source":"messages","message":"Access to this API resource is disabled for your application. Contact support."}`, want: ErrRestricted, wantMsg: "access_restricted"},
		{name: "406", status: 406, body: `"Traffic is suspended."`, want: ErrNotAcceptable, wantMsg: "Traffic is suspended."},
		{name: "409 in progress", status: 409, body: `{"message":"in_progress","retry_after":3}`, want: ErrInProgress, wantRetry: 3 * time.Second, wantMsg: "in_progress", hidden: []string{"in_progress", "3"}},
		{name: "409 in progress with a header", status: 409, body: `{"message":"in_progress","retry_after":3}`, header: map[string]string{"Retry-After": "5"}, want: ErrInProgress, wantRetry: 5 * time.Second, wantMsg: "in_progress"},
		{name: "422 field map", status: 422, body: `{"[phone]":"This value is not a valid mobile number.","[template]":"This field is missing."}`, want: ErrValidation, hidden: []string{"mobile"}},
		{name: "429 with a header", status: 429, body: `{"message":"rate_limited","retry_after":23}`, header: map[string]string{"Retry-After": "7"}, want: ErrRateLimited, wantRetry: 7 * time.Second, wantMsg: "rate_limited", hidden: []string{"rate_limited", "23", "7"}},
		{name: "429 per-phone plain string", status: 429, body: `"Too many OTPs."`, want: ErrRateLimited, wantMsg: "Too many OTPs."},
		{name: "451", status: 451, body: `"There is no signed contract for channel \"viber\""`, want: ErrNoContract, wantMsg: `There is no signed contract for channel "viber"`},
		{name: "502", status: 502, body: `"Could not parse the phone number."`, want: ErrUnprocessable, wantMsg: "Could not parse the phone number.", hidden: []string{"parse"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			err := sendOnce(t, c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want an *APIError", err)
			}
			if apiErr.Status != tt.status {
				t.Errorf("Status = %d, want %d", apiErr.Status, tt.status)
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("errors.Is(err, %v) is false", tt.want)
			}
			for _, other := range sentinels {
				if other != tt.want && errors.Is(err, other) {
					t.Errorf("errors.Is(err, %v) is true as well", other)
				}
			}
			if errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("a definite answer is outcome unknown")
			}
			if apiErr.RetryAfter != tt.wantRetry {
				t.Errorf("RetryAfter = %v, want %v", apiErr.RetryAfter, tt.wantRetry)
			}
			if apiErr.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", apiErr.Message, tt.wantMsg)
			}
			if string(apiErr.Raw) != tt.body {
				t.Errorf("Raw = %s, want the body served", apiErr.Raw)
			}
			if got := err.Error(); got != "connectix: HTTP "+strconv.Itoa(tt.status) {
				t.Errorf("Error() = %q", got)
			}
			for _, h := range append(tt.hidden, tt.wantMsg, tt.body) {
				if h != "" && strings.Contains(err.Error(), h) {
					t.Errorf("Error() carries %q: %v", h, err)
				}
			}
		})
	}
}

func TestAnotherBadRequestMatchesNoSentinel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"Invalid JSON: Syntax error"}`))
	})
	err := sendOnce(t, c)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Message != "Invalid JSON: Syntax error" {
		t.Fatalf("err = %v (%+v)", err, apiErr)
	}
	for _, s := range []error{ErrTemplateNotFound, ErrValidation, ErrUnprocessable, ErrOutcomeUnknown} {
		if errors.Is(err, s) {
			t.Errorf("a 400 with another reason matches %v", s)
		}
	}
}

func TestAnHTMLErrorBodyIsAnAPIErrorWithNoMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
	})
	err := sendOnce(t, c)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Status != 502 || apiErr.Message != "" || apiErr.RetryAfter != 0 {
		t.Errorf("error = %+v", apiErr)
	}
	if apiErr.Raw != nil {
		t.Errorf("Raw = %s, want nil for a body that is not JSON", apiErr.Raw)
	}
	if !errors.Is(err, ErrUnprocessable) {
		t.Error("a 502 is ErrUnprocessable whatever its body")
	}
	if strings.Contains(err.Error(), "html") {
		t.Errorf("Error() quotes the body: %v", err)
	}
}

func TestAnEmptyErrorBodyIsAnAPIErrorWithNoMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	err := sendOnce(t, c)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Message != "" || apiErr.Raw != nil {
		t.Fatalf("err = %v (%+v)", err, apiErr)
	}
}

func TestAnErrorBodyOfAnotherJSONShapeKeepsItsRawAndNoMessage(t *testing.T) {
	for name, body := range map[string]string{
		"an array":        `["a"]`,
		"a number":        `42`,
		"null":            `null`,
		"a typed error":   `{"error":{"code":1}}`,
		"a typed message": `{"message":7,"retry_after":"x"}`,
		"invalid JSON":    `{"error":`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(body))
			})
			err := sendOnce(t, c)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 402 || apiErr.Message != "" || apiErr.RetryAfter != 0 {
				t.Fatalf("err = %v (%+v)", err, apiErr)
			}
			if !errors.Is(err, ErrInsufficientFunds) {
				t.Error("a 402 is ErrInsufficientFunds whatever its body")
			}
		})
	}
}

func TestRetryAfterReadsSecondsADateAndRefusesNonsense(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		header string
		body   string
		want   time.Duration
	}{
		{"7", `"x"`, 7 * time.Second},
		{"0", `"x"`, 0},
		{"Mon, 02 Nov 2026 12:00:30 GMT", `"x"`, 30 * time.Second},
		{"Mon, 02 Nov 2026 11:00:00 GMT", `"x"`, 0},
		{"-5", `"x"`, 0},
		{"soon", `"x"`, 0},
		{"soon", `{"message":"rate_limited","retry_after":9}`, 9 * time.Second},
		{"", `{"message":"rate_limited","retry_after":-4}`, 0},
		{"", `{"message":"rate_limited","retry_after":2.5}`, 0},
		{"99999999999", `"x"`, maxRetryAfter},
		{"", `{"message":"rate_limited","retry_after":99999999999}`, maxRetryAfter},
	}
	for _, tt := range tests {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if tt.header != "" {
				w.Header().Set("Retry-After", tt.header)
			}
			w.WriteHeader(429)
			_, _ = w.Write([]byte(tt.body))
		}, withNow(func() time.Time { return now }))
		var apiErr *APIError
		if err := sendOnce(t, c); !errors.As(err, &apiErr) {
			t.Fatalf("err = %v", err)
		}
		if apiErr.RetryAfter != tt.want {
			t.Errorf("Retry-After %q, body %s: RetryAfter = %v, want %v", tt.header, tt.body, apiErr.RetryAfter, tt.want)
		}
	}
}

func TestAnAPIErrorMatchesNoSentinelOfAnotherStatus(t *testing.T) {
	e := &APIError{Status: 418}
	for _, s := range []error{ErrUnauthorized, ErrInsufficientFunds, ErrRestricted, ErrNotAcceptable, ErrInProgress,
		ErrValidation, ErrRateLimited, ErrNoContract, ErrUnprocessable, ErrTemplateNotFound, ErrOutcomeUnknown} {
		if errors.Is(e, s) {
			t.Errorf("a 418 matches %v", s)
		}
	}
}
