package connectix

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const integrationSecret = "VWAIKyRhzVmGkgDXTxepB1rO49CYKo3C" // 32 bytes, a placeholder

const integrationToken = "npu5K6DCMcsmLyDjLNpSkc8szq8iUSlY6Ga96vJjxsKOYWiAILbvQp9qXkKKGIfl" // 64 characters, a placeholder

func readJSON(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	return m
}

func TestRegisteringAnIntegrationPostsTheKindPlatformAndSecret(t *testing.T) {
	var method, path, auth, ctype, query string
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, auth, ctype, query = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.RawQuery
		body = readJSON(t, r)
		_, _ = w.Write([]byte(`{"name":"HuboGO","token":"` + integrationToken + `"}`))
	})
	got, err := c.RegisterIntegration(t.Context(), "custom", "HuboGO platform", []byte(integrationSecret))
	if err != nil {
		t.Fatalf("RegisterIntegration: %v", err)
	}
	if got != integrationToken {
		t.Errorf("token = %q, want the one served", got)
	}
	if method != http.MethodPost || path != "/integration/register" || query != "" {
		t.Errorf("request = %s %s ?%s, want POST /integration/register", method, path, query)
	}
	if auth != testToken || ctype != "application/json" {
		t.Errorf("headers: Authorization %q, Content-Type %q", auth, ctype)
	}
	want := map[string]any{"type": "custom", "platform": "HuboGO platform", "secret": integrationSecret}
	if len(body) != len(want) || body["type"] != want["type"] || body["platform"] != want["platform"] || body["secret"] != want["secret"] {
		t.Errorf("body keys = %v, want exactly type, platform and secret", keys(body))
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRegisteringAnIntegrationRefusesASecretThatIsNot32Bytes(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"name":"x","token":"` + integrationToken + `"}`))
	})
	bad := map[string][]byte{
		"nil":         nil,
		"31 bytes":    []byte(integrationSecret[:31]),
		"33 bytes":    []byte(integrationSecret + "x"),
		"a new line":  []byte(integrationSecret[:31] + "\n"),
		"not ASCII":   []byte(integrationSecret[:30] + "é"), // 32 bytes, 31 characters
		"a space end": []byte(integrationSecret[:31] + " "),
	}
	for name, secret := range bad {
		_, err := c.RegisterIntegration(t.Context(), "custom", "p", secret)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if len(secret) > 0 && strings.Contains(err.Error(), string(secret)) {
			t.Errorf("%s: the error quotes the secret", name)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a refused secret still sent %d request(s)", n)
	}
}

func TestRegisteringAnIntegrationRefusesAnUnknownKindOrAnEmptyPlatform(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, kind := range []string{"", "Custom", "shop", "ecommerce "} {
		if _, err := c.RegisterIntegration(t.Context(), kind, "p", []byte(integrationSecret)); err == nil {
			t.Errorf("kind %q accepted", kind)
		}
	}
	if _, err := c.RegisterIntegration(t.Context(), "custom", "", []byte(integrationSecret)); err == nil {
		t.Error("an empty platform accepted")
	}
	for _, kind := range []string{"custom", "ecommerce", "automation"} {
		c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := readJSON(t, r)["type"]; got != kind {
				t.Errorf("type sent = %v, want %s", got, kind)
			}
			_, _ = w.Write([]byte(`{"name":"x","token":"` + integrationToken + `"}`))
		})
		if _, err := c2.RegisterIntegration(t.Context(), kind, "p", []byte(integrationSecret)); err != nil {
			t.Errorf("kind %s: %v", kind, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a refused call still sent %d request(s)", n)
	}
}

func TestRegisteringAnIntegrationReportsTheAPIsRefusalsWithoutTheSecret(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`"This action needs a valid token!"`))
	})
	_, err := c.RegisterIntegration(t.Context(), "custom", "p", []byte(integrationSecret))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), integrationSecret) {
		t.Error("the error carries the secret")
	}
}

func TestAnIntegrationAnswerWithoutATokenIsOutcomeUnknown(t *testing.T) {
	for _, body := range []string{`{"name":"x"}`, `{"name":"x","token":""}`, `[]`} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		tok, err := c.RegisterIntegration(t.Context(), "custom", "p", []byte(integrationSecret))
		if !errors.Is(err, ErrOutcomeUnknown) || tok != "" {
			t.Errorf("body %s: token %q, err %v; want no token and ErrOutcomeUnknown", body, tok, err)
		}
	}
}

func TestRegisteringAHookPostsTheTokenTypeAndURL(t *testing.T) {
	var method, path, query string
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		body = readJSON(t, r)
		_, _ = w.Write([]byte(`{"id":"f4212b48-a763-42b8-8e43-0ed413255edd"}`))
	})
	id, err := c.RegisterHook(t.Context(), integrationToken, "callback", "https://hooks.example.com/connectix")
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}
	if id != "f4212b48-a763-42b8-8e43-0ed413255edd" {
		t.Errorf("id = %q", id)
	}
	if method != http.MethodPost || path != "/integration/register/hook" || query != "" {
		t.Errorf("request = %s %s ?%s, want POST /integration/register/hook with no query (the token is in the body)", method, path, query)
	}
	if len(body) != 3 || body["token"] != integrationToken || body["type"] != "callback" || body["url"] != "https://hooks.example.com/connectix" {
		t.Errorf("body keys = %v, want exactly token, type and url", keys(body))
	}
}

func TestRegisteringAHookRefusesWhatWouldChangeTheRequest(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	tests := []struct{ name, token, typ, url string }{
		{"no token", "", "callback", "https://h.example/x"},
		{"unknown type", integrationToken, "status", "https://h.example/x"},
		{"empty type", integrationToken, "", "https://h.example/x"},
		{"no url", integrationToken, "callback", ""},
	}
	for _, tt := range tests {
		_, err := c.RegisterHook(t.Context(), tt.token, tt.typ, tt.url)
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
			continue
		}
		if tt.token != "" && strings.Contains(err.Error(), tt.token) {
			t.Errorf("%s: the error quotes the token", tt.name)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a refused call still sent %d request(s)", n)
	}
	for _, typ := range []string{"callback", "inbound", "template", "tracking", "blacklist"} {
		c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id":"h1"}`)) })
		if _, err := c2.RegisterHook(t.Context(), integrationToken, typ, "https://h.example/x"); err != nil {
			t.Errorf("type %s: %v", typ, err)
		}
	}
}

func TestAHookRefusedForANonPublicURLIsAValidationError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"url":["The URL must be a public http(s) address."]}`))
	})
	_, err := c.RegisterHook(t.Context(), integrationToken, "callback", "http://10.0.0.1/x")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if strings.Contains(err.Error(), integrationToken) || strings.Contains(err.Error(), "10.0.0.1") {
		t.Errorf("error %q carries the token or the URL", err)
	}
}

func TestAHookAnswerWithoutAnIDIsOutcomeUnknown(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	id, err := c.RegisterHook(t.Context(), integrationToken, "callback", "https://h.example/x")
	if !errors.Is(err, ErrOutcomeUnknown) || id != "" {
		t.Errorf("id %q, err %v; want no id and ErrOutcomeUnknown", id, err)
	}
}

func TestUnregisteringAHookPostsItsID(t *testing.T) {
	var method, path string
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body = readJSON(t, r)
		_, _ = w.Write([]byte(`"ok"`))
	})
	if err := c.UnregisterHook(t.Context(), "f4212b48-a763-42b8-8e43-0ed413255edd"); err != nil {
		t.Fatalf("UnregisterHook: %v", err)
	}
	if method != http.MethodPost || path != "/integration/unregister/hook" {
		t.Errorf("request = %s %s, want POST /integration/unregister/hook", method, path)
	}
	if len(body) != 1 || body["id"] != "f4212b48-a763-42b8-8e43-0ed413255edd" {
		t.Errorf("body = %v, want exactly the id", body)
	}
}

func TestUnregisteringAHookRefusesAnEmptyIDAndReportsANotFound(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`"Not found."`))
	})
	if err := c.UnregisterHook(t.Context(), ""); err == nil || calls.Load() != 0 {
		t.Errorf("an empty id: err %v, %d request(s); want a local refusal", err, calls.Load())
	}
	err := c.UnregisterHook(t.Context(), "h1")
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusNotFound {
		t.Errorf("err = %v, want an *APIError with status 404", err)
	}
}

func TestAnUnregisterAnswerThatIsNotOkIsOutcomeUnknown(t *testing.T) {
	for _, body := range []string{`"nope"`, `{}`, `5`} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if err := c.UnregisterHook(t.Context(), "h1"); !errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("body %s: err = %v, want ErrOutcomeUnknown", body, err)
		}
	}
}
