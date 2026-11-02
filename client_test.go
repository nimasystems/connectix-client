package connectix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testToken is a placeholder, never a real token. Its first characters are
// what the redaction tests look for.
const testToken = "tok-0123456789abcdef"

// newTestClient serves handler on a loopback httptest server and returns a
// client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(testToken, append([]Option{WithBaseURL(srv.URL)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRefusesAMissingOrPlainHTTPBaseURL(t *testing.T) {
	tests := []struct {
		name  string
		token string
		opts  []Option
		ok    bool
	}{
		{"no base URL at all", testToken, nil, false},
		{"https", testToken, []Option{WithBaseURL("https://api-sandbox.connectix.bg")}, true},
		{"http on loopback", testToken, []Option{WithBaseURL("http://127.0.0.1:8080")}, true},
		{"http on localhost", testToken, []Option{WithBaseURL("http://localhost:8080")}, true},
		{"http on an IPv6 loopback", testToken, []Option{WithBaseURL("http://[::1]:8080")}, true},
		{"http on a public host", testToken, []Option{WithBaseURL("http://api.connectix.bg")}, false},
		{"a relative URL", testToken, []Option{WithBaseURL("/messages")}, false},
		{"an empty URL", testToken, []Option{WithBaseURL("")}, false},
		{"userinfo in the URL", testToken, []Option{WithBaseURL("https://u:p@api-sandbox.connectix.bg")}, false},
		{"a nil HTTP client", testToken, []Option{WithBaseURL("https://x.example"), WithHTTPClient(nil)}, false},
		{"an empty token", "", []Option{WithBaseURL("https://x.example")}, false},
		{"a token with a space", "tok en", []Option{WithBaseURL("https://x.example")}, false},
		{"a token with a newline", "tok\nen", []Option{WithBaseURL("https://x.example")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.token, tt.opts...)
			if tt.ok {
				if err != nil || c == nil {
					t.Fatalf("New: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("New succeeded, want an error")
			}
			for _, secret := range []string{tt.token, "u:p", "api.connectix.bg"} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Errorf("the error echoes %q: %v", secret, err)
				}
			}
		})
	}
}

func TestTheClientNeverPrintsItsTokenWithAnyVerb(t *testing.T) {
	c, err := New(testToken, WithBaseURL("https://api-sandbox.connectix.bg"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The first 3 characters catch a precision verb such as %9.3f, which
	// prints only part of a string.
	needle := testToken[:3]
	verbs := []string{"%d", "%t", "%U", "%9.3f", "%p", "%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%T", "%e", "%c", "%o", "%b"}
	values := map[string]any{"*Client": c, "Client": *c, "token": c.token}
	for _, verb := range verbs {
		for name, v := range values {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, needle) {
				t.Errorf("fmt %s of %s prints the token: %s", verb, name, out)
			}
		}
	}
	for name, v := range values {
		var text, js bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("client", "v", v)
		slog.New(slog.NewJSONHandler(&js, nil)).Info("client", "v", v)
		if strings.Contains(text.String(), needle) || strings.Contains(js.String(), needle) {
			t.Errorf("slog of %s prints the token: %s %s", name, text.String(), js.String())
		}
	}
	text, err := c.token.MarshalText()
	if err != nil || strings.Contains(string(text), needle) {
		t.Errorf("MarshalText = %q, %v; want a redaction", text, err)
	}
	if got := fmt.Sprintf("%v", c); !strings.Contains(got, "api-sandbox.connectix.bg") {
		t.Errorf("a client prints as %q, want its host", got)
	}
	if got := fmt.Sprintf("%v", Client{}); strings.Contains(got, "nil") || got == "" {
		t.Errorf("a zero client prints as %q", got)
	}
}

func TestTheClientSendsTheRawTokenOnlyInTheAuthorizationHeader(t *testing.T) {
	var seen *http.Request
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r
		_, _ = w.Write([]byte(`{"name":"Shop"}`))
	})
	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if seen.Method != http.MethodGet || seen.URL.Path != "/me" || seen.URL.RawQuery != "" {
		t.Errorf("request = %s %s, want GET /me", seen.Method, seen.URL)
	}
	if got := seen.Header.Get("Authorization"); got != testToken {
		t.Errorf("Authorization = %q, want the raw token with no Bearer prefix", got)
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := seen.Header.Get("Content-Type"); got != "" {
		t.Errorf("a GET sent Content-Type %q", got)
	}
	if got := seen.Header.Get("Idempotency-Key"); got != "" {
		t.Errorf("a GET sent Idempotency-Key %q", got)
	}
	for name, values := range seen.Header {
		if name == "Authorization" {
			continue
		}
		for _, v := range values {
			if strings.Contains(v, testToken) {
				t.Errorf("header %s carries the token", name)
			}
		}
	}
	if strings.Contains(seen.URL.String(), testToken) {
		t.Error("the token is in the URL")
	}
}

func TestABaseURLWithAPathPrefixIsKept(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"name":"Shop"}`))
	}))
	defer srv.Close()
	c, err := New(testToken, WithBaseURL(srv.URL+"/proxy"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Me(t.Context()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if path != "/proxy/me" {
		t.Errorf("path = %q, want /proxy/me", path)
	}
}

func TestAZeroOrNilClientReturnsAnErrorInsteadOfPanicking(t *testing.T) {
	var nilClient *Client
	for name, c := range map[string]*Client{"nil": nilClient, "zero": {}} {
		if _, err := c.Me(t.Context()); err == nil {
			t.Errorf("%s client: Me succeeded", name)
		}
		if _, err := c.ListTemplates(t.Context()); err == nil {
			t.Errorf("%s client: ListTemplates succeeded", name)
		}
		if _, err := c.GetMessage(t.Context(), "m1"); err == nil {
			t.Errorf("%s client: GetMessage succeeded", name)
		}
		if _, err := c.SendMessage(t.Context(), SendMessageRequest{}, ""); err == nil {
			t.Errorf("%s client: SendMessage succeeded", name)
		}
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	hits := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/elsewhere" {
			t.Error("the redirect was followed")
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want an APIError with status 307", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

func TestATransportFailureIsOutcomeUnknown(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Read the request, then drop the connection with no answer.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	})
	_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("a transport failure is an APIError: %v", err)
	}
}

func TestATimeoutAfterTheRequestLeftIsOutcomeUnknown(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // reading the body lets the server notice the client leaving
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(ctx, SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to unwrap to context.DeadlineExceeded", err)
	}
}

func TestAConnectionRefusedBeforeSendingIsNotOutcomeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there any more
	c, err := New(testToken, WithBaseURL(url))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	if err == nil {
		t.Fatal("SendMessage succeeded against a closed port")
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("a refused dial is outcome unknown: %v", err)
	}
}

func TestAContextAlreadyDoneNeverSendsAndIsNotOutcomeUnknown(t *testing.T) {
	hits := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.SendMessage(ctx, SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("a request that never left is outcome unknown: %v", err)
	}
	if hits != 0 {
		t.Errorf("hits = %d, want 0", hits)
	}
}

func TestATransportErrorNeverCarriesTheIntegrationTokenOrTheURL(t *testing.T) {
	const integration = "int-0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	})
	_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1", Integration: integration}, "k1")
	if err == nil {
		t.Fatal("SendMessage succeeded")
	}
	for _, leak := range []string{integration, "integration=", testToken, "?"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error carries %q: %v", leak, err)
		}
	}
}

func TestAnAnswerThatIsNotTheExpectedJSONIsOutcomeUnknown(t *testing.T) {
	for name, body := range map[string]string{
		"html":            `<html>ok</html>`,
		"an empty body":   ``,
		"the wrong shape": `[1,2,3]`,
		"no id":           `{}`,
		"null":            `null`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			_, err := c.SendMessage(t.Context(), SendMessageRequest{Phone: "+359881234567", Template: "t1"}, "k1")
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("err = %v, want ErrOutcomeUnknown (the message may have been accepted)", err)
			}
			if strings.Contains(err.Error(), "html") || strings.Contains(err.Error(), "[1,2,3]") {
				t.Errorf("the error quotes the body: %v", err)
			}
		})
	}
}

func TestAnOversizedAnswerIsRefusedNotBuffered(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"`))
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxBodyBytes+10))
		_, _ = w.Write([]byte(`"}`))
	})
	if _, err := c.Me(t.Context()); err == nil {
		t.Fatal("Me succeeded on an answer over the cap")
	}
}
