package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const (
	testToken        = "tok-0123456789abcdef"
	integrationToken = "npu5K6DCMcsmLyDjLNpSkc8szq8iUSlY6Ga96vJjxsKOYWiAILbvQp9qXkKKGIfl"
	secret32         = "VWAIKyRhzVmGkgDXTxepB1rO49CYKo3C"
)

type result struct {
	code        int
	out, errOut string
}

// harness serves handler on loopback and runs the command against it with
// the environment the command reads.
type harness struct {
	t   *testing.T
	url string
	env map[string]string
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &harness{t: t, url: srv.URL, env: map[string]string{"CONNECTIX_TOKEN": testToken, "CONNECTIX_BASE_URL": srv.URL}}
}

func (h *harness) run(stdin string, args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut, func(k string) string { return h.env[k] })
	return result{code, out.String(), errOut.String()}
}

func noCalls(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func TestTheTemplatesCommandNeverPrintsTheText(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/templates" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
		 {"id":"a1","name":"Code","channel":"sms","status":"approved","text":"HuboGO: your code is {{ code }}, valid {{ minutes }} minutes"},
		 {"id":"a2","name":"Plain","channel":"viber","status":"pending","text":"Secret promo wording"},
		 {"id":"a3","name":"Empty","channel":"sms","status":"rejected","text":null}]`))
	})
	got := h.run("", "templates")
	if got.code != 0 {
		t.Fatalf("exit %d, stderr %q", got.code, got.errOut)
	}
	want := "a1\tCode\tsms\tapproved\tcode,minutes\na2\tPlain\tviber\tpending\t\na3\tEmpty\tsms\trejected\t\n"
	if got.out != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got.out, want)
	}
	for _, text := range []string{"your code is", "Secret promo", "{{"} {
		if strings.Contains(got.out+got.errOut, text) {
			t.Errorf("the output carries template text %q", text)
		}
	}
}

func TestTheMeCommandPrintsTheApplicationName(t *testing.T) {
	var auth string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"name":"HuboGO"}`))
	})
	got := h.run("", "me")
	if got.code != 0 || got.out != "HuboGO\n" || got.errOut != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", got.code, got.out, got.errOut)
	}
	if auth != testToken {
		t.Errorf("Authorization = %q, want the token from CONNECTIX_TOKEN", auth)
	}
}

func TestRegisterIntegrationWritesTheTokenToItsFileAlone(t *testing.T) {
	var body string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.URL.Path != "/integration/register" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"HuboGO","token":"` + integrationToken + `"}`))
	})
	file := filepath.Join(t.TempDir(), "integration.token")
	got := h.run(secret32+"\n", "register-integration", "--platform", "HuboGO platform", "--out", file)
	if got.code != 0 {
		t.Fatalf("exit %d, stderr %q", got.code, got.errOut)
	}
	for _, leak := range []string{integrationToken, secret32, testToken} {
		if strings.Contains(got.out, leak) || strings.Contains(got.errOut, leak) {
			t.Errorf("the output carries a secret value (%d characters)", len(leak))
		}
	}
	saved, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read the token file: %v", err)
	}
	if string(saved) != integrationToken+"\n" {
		t.Errorf("the token file does not hold the integration token (%d bytes)", len(saved))
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(file)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if !strings.Contains(body, `"type":"custom"`) || !strings.Contains(body, `"platform":"HuboGO platform"`) || !strings.Contains(body, secret32) {
		t.Errorf("the request body was not the expected registration")
	}
}

func TestRegisterIntegrationTakesTheKindAndRefusesToOverwriteTheFile(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"type":"ecommerce"`) {
			t.Errorf("kind not sent: %s", b)
		}
		_, _ = w.Write([]byte(`{"name":"x","token":"` + integrationToken + `"}`))
	})
	file := filepath.Join(t.TempDir(), "t")
	if got := h.run(secret32, "register-integration", "--platform", "p", "--kind", "ecommerce", "--out", file); got.code != 0 {
		t.Fatalf("first run: exit %d, stderr %q", got.code, got.errOut)
	}
	// A second run would replace a token somebody may still need; and the API
	// answers the same token for the same secret anyway.
	again := h.run(secret32, "register-integration", "--platform", "p", "--kind", "ecommerce", "--out", file)
	if again.code == 0 || !strings.Contains(again.errOut, "exists") {
		t.Errorf("second run: exit %d, stderr %q; want a refusal that says the file exists", again.code, again.errOut)
	}
}

func TestRegisterIntegrationRefusesBeforeCallingWhenItsInputIsIncomplete(t *testing.T) {
	h := newHarness(t, noCalls(t))
	dir := t.TempDir()
	tests := map[string]struct {
		stdin string
		args  []string
	}{
		"no --out":           {secret32, []string{"register-integration", "--platform", "p"}},
		"no --platform":      {secret32, []string{"register-integration", "--out", filepath.Join(dir, "a")}},
		"an empty secret":    {"", []string{"register-integration", "--platform", "p", "--out", filepath.Join(dir, "b")}},
		"a short secret":     {"short\n", []string{"register-integration", "--platform", "p", "--out", filepath.Join(dir, "c")}},
		"an unknown --kind":  {secret32, []string{"register-integration", "--platform", "p", "--kind", "shop", "--out", filepath.Join(dir, "d")}},
		"a stray argument":   {secret32, []string{"register-integration", "--platform", "p", "--out", filepath.Join(dir, "e"), "extra"}},
		"an unwritable path": {secret32, []string{"register-integration", "--platform", "p", "--out", filepath.Join(dir, "missing", "f")}},
	}
	for name, tt := range tests {
		got := h.run(tt.stdin, tt.args...)
		if got.code == 0 {
			t.Errorf("%s: exit 0", name)
		}
		if strings.Contains(got.out+got.errOut, secret32) {
			t.Errorf("%s: the output carries the secret", name)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refused run left %d file(s) behind", len(entries))
	}
}

func TestAFailedRegistrationLeavesNoTokenFileAndNoSecretInTheOutput(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`"This action needs a valid token!"`))
	})
	file := filepath.Join(t.TempDir(), "t")
	got := h.run(secret32, "register-integration", "--platform", "p", "--out", file)
	if got.code == 0 || !strings.Contains(got.errOut, "401") {
		t.Errorf("exit %d, stderr %q; want a failure naming the status", got.code, got.errOut)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a token file exists after a failed registration")
	}
	for _, leak := range []string{secret32, testToken} {
		if strings.Contains(got.out+got.errOut, leak) {
			t.Error("the output carries a secret value")
		}
	}
}

func TestRegisterHookReadsTheTokenFromStandardInputAndPrintsOnlyTheID(t *testing.T) {
	var body string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.URL.Path != "/integration/register/hook" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"f4212b48-a763-42b8-8e43-0ed413255edd"}`))
	})
	got := h.run(integrationToken+"\n", "register-hook", "--url", "https://hooks.example.com/c")
	if got.code != 0 || got.out != "f4212b48-a763-42b8-8e43-0ed413255edd\n" || got.errOut != "" {
		t.Errorf("exit %d, stdout %q, stderr %q", got.code, got.out, got.errOut)
	}
	if !strings.Contains(body, `"type":"callback"`) || !strings.Contains(body, `"url":"https://hooks.example.com/c"`) || !strings.Contains(body, integrationToken) {
		t.Errorf("unexpected request body")
	}
	// --type picks another hook type.
	h2 := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"type":"inbound"`) {
			t.Errorf("type not sent: %s", b)
		}
		_, _ = w.Write([]byte(`{"id":"h2"}`))
	})
	if got := h2.run(integrationToken, "register-hook", "--url", "https://h.example/x", "--type", "inbound"); got.code != 0 || got.out != "h2\n" {
		t.Errorf("--type inbound: exit %d, stdout %q, stderr %q", got.code, got.out, got.errOut)
	}
}

func TestRegisterHookRefusesBeforeCallingWhenItsInputIsIncomplete(t *testing.T) {
	h := newHarness(t, noCalls(t))
	tests := map[string]struct {
		stdin string
		args  []string
	}{
		"no --url":          {integrationToken, []string{"register-hook"}},
		"an empty token":    {"", []string{"register-hook", "--url", "https://h.example/x"}},
		"an unknown --type": {integrationToken, []string{"register-hook", "--url", "https://h.example/x", "--type", "status"}},
	}
	for name, tt := range tests {
		got := h.run(tt.stdin, tt.args...)
		if got.code == 0 {
			t.Errorf("%s: exit 0", name)
		}
		if strings.Contains(got.out+got.errOut, integrationToken) {
			t.Errorf("%s: the output carries the integration token", name)
		}
	}
}

func TestUnregisterHookPostsTheIDAndSaysSo(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/integration/unregister/hook" || !strings.Contains(string(b), `"id":"h1"`) {
			t.Errorf("request = %s %s", r.URL.Path, b)
		}
		_, _ = w.Write([]byte(`"ok"`))
	})
	got := h.run("", "unregister-hook", "--id", "h1")
	if got.code != 0 || got.out != "unregistered h1\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", got.code, got.out, got.errOut)
	}
	if got := h.run("", "unregister-hook"); got.code == 0 {
		t.Error("unregister-hook without --id exited 0")
	}
}

func TestTheCommandRefusesWithoutItsEnvironmentOrAKnownCommand(t *testing.T) {
	h := newHarness(t, noCalls(t))
	for name, mutate := range map[string]func(){
		"no token":         func() { delete(h.env, "CONNECTIX_TOKEN") },
		"no base URL":      func() { delete(h.env, "CONNECTIX_BASE_URL") },
		"a plain-http URL": func() { h.env["CONNECTIX_BASE_URL"] = "http://api.connectix.bg" },
	} {
		saved := map[string]string{}
		for k, v := range h.env {
			saved[k] = v
		}
		mutate()
		got := h.run("", "me")
		if got.code == 0 {
			t.Errorf("%s: exit 0", name)
		}
		if name == "no token" && !strings.Contains(got.errOut, "CONNECTIX_TOKEN") {
			t.Errorf("%s: stderr %q does not name the variable", name, got.errOut)
		}
		if strings.Contains(got.out+got.errOut, testToken) {
			t.Errorf("%s: the output carries the token", name)
		}
		h.env = saved
	}
	for _, args := range [][]string{{"me", "extra"}, {"templates", "extra"}} {
		if got := h.run("", args...); got.code == 0 {
			t.Errorf("args %v: exit 0", args)
		}
	}
	for _, args := range [][]string{nil, {"bogus"}, {"-h"}} {
		got := h.run("", args...)
		if got.code == 0 {
			t.Errorf("args %v: exit 0", args)
		}
		if !strings.Contains(got.errOut, "register-integration") {
			t.Errorf("args %v: stderr %q does not list the commands", args, got.errOut)
		}
	}
}

func TestACommandFailureNamesTheStatusAndNeverTheBody(t *testing.T) {
	var mu sync.Mutex
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"url":["The URL must be a public http(s) address: http://10.0.0.1/x"]}`))
	})
	got := h.run(integrationToken, "register-hook", "--url", "http://10.0.0.1/x")
	if got.code != 1 || !strings.Contains(got.errOut, "422") {
		t.Errorf("exit %d, stderr %q; want exit 1 naming 422", got.code, got.errOut)
	}
	for _, leak := range []string{"10.0.0.1", integrationToken, testToken} {
		if strings.Contains(got.out+got.errOut, leak) {
			t.Errorf("the output carries %q", leak)
		}
	}
}

func TestTheBalanceCommandPrintsTheAmounts(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/balance" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"currency":"EUR","total":"203.4500","threshold":"50.0000","lowBalance":true,"channels":{"viber":{"available":"23.4500","own":"23.4500","pool":null,"low":true},"sms":{"available":"180.0000","own":"100.0000","pool":"80.0000","low":false}},"pools":[{"name":"Main top-up","channel":"sms","balance":"80.0000"}]}`))
	})
	got := h.run("", "balance")
	if got.code != 0 {
		t.Fatalf("exit %d, stderr %q", got.code, got.errOut)
	}
	want := "currency\tEUR\ntotal\t203.4500\nthreshold\t50.0000\nlowBalance\ttrue\nsms\t180.0000\tfalse\nviber\t23.4500\ttrue\n"
	if got.out != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got.out, want)
	}
	if strings.Contains(got.out+got.errOut, testToken) {
		t.Error("the output carries the token")
	}
}

func TestTheBalanceCommandPrintsNoThresholdAsADash(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"currency":"EUR","total":"0.0000","threshold":null,"lowBalance":false,"channels":{},"pools":[]}`))
	})
	got := h.run("", "balance")
	want := "currency\tEUR\ntotal\t0.0000\nthreshold\t-\nlowBalance\tfalse\n"
	if got.code != 0 || got.out != want {
		t.Errorf("exit %d, stdout %q, want %q", got.code, got.out, want)
	}
}

func TestTheBalanceCommandReportsAFailureWithoutTheBody(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`"secret reason"`))
	})
	got := h.run("", "balance")
	if got.code != 1 || !strings.Contains(got.errOut, "HTTP 401") || strings.Contains(got.errOut, "secret reason") {
		t.Errorf("exit %d, stderr %q", got.code, got.errOut)
	}
}

func TestTheBalanceCommandTakesNoArguments(t *testing.T) {
	h := newHarness(t, noCalls(t))
	if got := h.run("", "balance", "extra"); got.code != 2 {
		t.Errorf("exit %d, want 2", got.code)
	}
}
