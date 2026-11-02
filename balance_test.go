package connectix

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// balanceBody is the example of the 1.3.0 document's getBalance.
const balanceBody = `{"currency":"EUR","total":"203.4500","threshold":"50.0000","lowBalance":true,"channels":{"sms":{"available":"180.0000","own":"100.0000","pool":"80.0000","low":false},"viber":{"available":"23.4500","own":"23.4500","pool":null,"low":true}},"pools":[{"name":"Main top-up","channel":"sms","balance":"80.0000"}]}`

func TestTheBalanceIsReadWithEveryChannel(t *testing.T) {
	var method, path, query string
	var header http.Header
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, query, header = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		w.Header().Set("X-RateLimit-Remaining", "58")
		_, _ = w.Write([]byte(balanceBody))
	})
	b, err := c.Balance(t.Context())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if method != http.MethodGet || path != "/balance" || query != "" {
		t.Errorf("request = %s %s?%s, want GET /balance", method, path, query)
	}
	if got := header.Get("Authorization"); got != testToken {
		t.Errorf("Authorization = %q", got)
	}
	if b.Currency != "EUR" || b.Total != "203.4500" || b.Threshold == nil || *b.Threshold != "50.0000" || !b.LowBalance {
		t.Errorf("balance = %+v", b)
	}
	if len(b.Channels) != 2 {
		t.Fatalf("channels = %+v", b.Channels)
	}
	sms := b.Channels["sms"]
	if sms.Available != "180.0000" || sms.Own != "100.0000" || sms.Pool == nil || *sms.Pool != "80.0000" || sms.Low {
		t.Errorf("sms = %+v", sms)
	}
	viber := b.Channels["viber"]
	if viber.Available != "23.4500" || viber.Own != "23.4500" || viber.Pool != nil || !viber.Low {
		t.Errorf("viber = %+v", viber)
	}
	if len(b.Pools) != 1 || b.Pools[0] != (BalancePool{Name: "Main top-up", Channel: "sms", Balance: "80.0000"}) {
		t.Errorf("pools = %+v", b.Pools)
	}
	if b.RateLimitRemaining != 58 {
		t.Errorf("RateLimitRemaining = %d, want 58", b.RateLimitRemaining)
	}
	if string(b.Raw) != balanceBody {
		t.Errorf("Raw = %s", b.Raw)
	}
}

func TestTheBalanceRateLimitIsMinusOneWhenTheHeaderIsAbsent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(balanceBody))
	})
	b, err := c.Balance(t.Context())
	if err != nil || b.RateLimitRemaining != -1 {
		t.Errorf("Balance = remaining %d, %v; want -1", b.RateLimitRemaining, err)
	}
}

func TestANullThresholdIsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"EUR","total":"10.0000","threshold":null,"lowBalance":false,"channels":{"sms":{"available":"10.0000","own":"10.0000","pool":null,"low":false}},"pools":[]}`))
	})
	b, err := c.Balance(t.Context())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if b.Threshold != nil || b.LowBalance {
		t.Errorf("threshold = %v, lowBalance = %v; want nil and false", b.Threshold, b.LowBalance)
	}
}

func TestNoActiveChannelIsAnEmptyMap(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"EUR","total":"0.0000","threshold":null,"lowBalance":false,"channels":{},"pools":[]}`))
	})
	b, err := c.Balance(t.Context())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if b.Channels == nil || len(b.Channels) != 0 {
		t.Errorf("channels = %#v, want an empty, non-nil map", b.Channels)
	}
	if _, ok := b.Channels["sms"]; ok {
		t.Error("an absent channel is present")
	}
}

func TestAvailableCentsReadsTheDecimal(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		refused bool
	}{
		{in: "20.8300", want: 2083},
		{in: "0.0000", want: 0},
		{in: "0.0049", want: 0},
		{in: "0.0100", want: 1},
		{in: "180", want: 18000},
		{in: "12.5", want: 1250},
		{in: "-1.0000", want: -100},
		{in: "-0.0049", want: 0},
		{in: "-0.0150", want: -1},
		{in: "+3.20", want: 320},
		{in: "abc", refused: true},
		{in: "", refused: true},
		{in: "1e3", refused: true},
		{in: ".", refused: true},
		{in: "-", refused: true},
		{in: "1.2.3", refused: true},
		{in: "1,50", refused: true},
		{in: " 1.00", refused: true},
		{in: "99999999999999999999", refused: true},
		{in: "92233720368547758.07", refused: true},
	}
	for _, tt := range tests {
		got, err := ChannelBalance{Available: tt.in}.AvailableCents()
		if tt.refused {
			if err == nil {
				t.Errorf("AvailableCents(%q) = %d, want an error", tt.in, got)
			} else if tt.in != "" && strings.Contains(err.Error(), tt.in) {
				t.Errorf("the error quotes the amount %q: %v", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("AvailableCents(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestTheBalanceStatusesMapToSentinels(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header map[string]string
		body   string
		want   error
		retry  time.Duration
	}{
		{name: "401", status: 401, body: `"Unauthorized."`, want: ErrUnauthorized},
		{name: "429 with a header", status: 429, header: map[string]string{"Retry-After": "9"}, body: `{"message":"rate_limited"}`, want: ErrRateLimited, retry: 9 * time.Second},
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
			_, err := c.Balance(t.Context())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var api *APIError
			if !errors.As(err, &api) || api.Status != tt.status || api.RetryAfter != tt.retry {
				t.Errorf("api = %+v", api)
			}
		})
	}
}

func TestABalanceThatCannotBeReadIsAnUnexpectedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"html":          `<html>proxy</html>`,
		"empty":         ``,
		"not an object": `[]`,
		"no currency":   `{"total":"1.0000","channels":{},"pools":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			_, err := c.Balance(t.Context())
			if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "proxy") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestABalanceOnAZeroClientIsAnError(t *testing.T) {
	var c *Client
	if _, err := c.Balance(t.Context()); err == nil {
		t.Error("want an error")
	}
}
