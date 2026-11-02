package connectix

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

const defaultSandboxBaseURL = "https://api-sandbox.connectix.bg"

// digitsFor fills a template slot with digits, so the text is a valid
// message whatever the slot is for.
func digitsFor(slot string) string {
	switch slot {
	case "code":
		return "123456"
	case "minutes":
		return "10"
	}
	return "1"
}

// isSandboxHost reports whether base names a host this test may send to: one
// whose name contains "sandbox", or a loopback host. The test sends a message
// to a real-looking mobile number, so it must never reach the live host,
// whatever CONNECTIX_SANDBOX_BASE_URL says.
func isSandboxHost(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return false
	}
	return strings.Contains(strings.ToLower(u.Hostname()), "sandbox") || isLoopback(u.Hostname())
}

func TestTheSandboxTestSendsOnlyToASandboxOrLoopbackHost(t *testing.T) {
	tests := map[string]bool{
		defaultSandboxBaseURL:              true,
		"https://API-SANDBOX.connectix.bg": true,
		"http://127.0.0.1:8080":            true,
		"http://localhost:8080":            true,
		"http://[::1]:8080":                true,
		"https://api.connectix.bg":         false, // the live host
		"https://api.connectix.bg/sandbox": false, // "sandbox" in the path is not the host
		"https://sandbox@api.connectix.bg": false, // nor is it the userinfo
		"https://example.com":              false,
		"":                                 false,
		"not a url":                        false,
		"/sandbox":                         false,
	}
	for base, want := range tests {
		if got := isSandboxHost(base); got != want {
			t.Errorf("isSandboxHost(%q) = %v, want %v", base, got, want)
		}
	}
}

// TestTheSandboxAcceptsASendAndReturnsItsStatus talks to the real sandbox. It
// runs only when CONNECTIX_SANDBOX_TOKEN is set (CONNECTIX_SANDBOX_BASE_URL
// is optional and defaults to the sandbox host) and is skipped otherwise, so
// CI needs no secret. It fails, and sends nothing, when the base URL is
// neither a sandbox host nor loopback. It calls Me, Balance, ListTemplates,
// SendMessage and GetMessage only (Balance is a read): the sandbox shares the company's real
// templates, and delivers and bills nothing.
func TestTheSandboxAcceptsASendAndReturnsItsStatus(t *testing.T) {
	token := os.Getenv("CONNECTIX_SANDBOX_TOKEN")
	if token == "" {
		t.Skip("CONNECTIX_SANDBOX_TOKEN is not set")
	}
	base := os.Getenv("CONNECTIX_SANDBOX_BASE_URL")
	if base == "" {
		base = defaultSandboxBaseURL
	}
	if !isSandboxHost(base) {
		t.Fatal("CONNECTIX_SANDBOX_BASE_URL names a host that is neither a sandbox nor loopback: refusing to send") // never echo the URL
	}
	c, err := New(token, WithBaseURL(base))
	if err != nil {
		t.Fatalf("New: %v", err) // New never echoes the token
	}
	ctx := t.Context()

	name, err := c.Me(ctx)
	if err != nil || name == "" {
		t.Fatalf("Me = %q, %v; want the application's name", name, err)
	}

	// The sandbox token returns the application's real balance and a sandbox
	// send never changes it. The amounts are not secrets, but none is logged.
	bal, err := c.Balance(ctx)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Currency != "EUR" || !regexp.MustCompile(`^-?\d+\.\d{4}$`).MatchString(bal.Total) || bal.Channels == nil {
		t.Errorf("Balance = currency %q, total shaped %v, channels nil %v", bal.Currency, regexp.MustCompile(`^-?\d+\.\d{4}$`).MatchString(bal.Total), bal.Channels == nil)
	}
	for name, ch := range bal.Channels {
		if _, err := ch.AvailableCents(); err != nil {
			t.Errorf("channel %s: AvailableCents: %v", name, err)
		}
	}
	if strings.Contains(string(bal.Raw), token) {
		t.Error("the balance answer carries the token")
	}

	templates, err := c.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	var tpl *Template
	for i := range templates {
		if templates[i].Status == "approved" && templates[i].Channel == "sms" {
			tpl = &templates[i]
			break
		}
	}
	if tpl == nil {
		t.Fatalf("the sandbox has no approved SMS template among %d templates", len(templates))
	}
	params := map[string]string{}
	for _, slot := range tpl.Slots {
		params[slot] = digitsFor(slot)
	}

	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	key := "sandbox-test-" + hex.EncodeToString(raw[:])
	req := SendMessageRequest{Phone: "+359881234567", Template: tpl.ID, Parameters: params}

	sent, err := c.SendMessage(ctx, req, key)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent.ID == "" || sent.Status == "" || sent.Channel != "sms" || sent.Replayed {
		t.Fatalf("SendMessage = id %q, status %q, channel %q, replayed %v", sent.ID, sent.Status, sent.Channel, sent.Replayed)
	}
	if !regexp.MustCompile(`^\d+\.\d{4}$`).MatchString(sent.Price) || sent.Price != "0.0000" {
		t.Errorf("price = %q, want 0.0000 on the sandbox", sent.Price)
	}
	if sent.CreatedAt.IsZero() || time.Since(sent.CreatedAt) > time.Hour {
		t.Errorf("CreatedAt = %v", sent.CreatedAt)
	}

	// The same key returns the stored answer and sends nothing again.
	again, err := c.SendMessage(ctx, req, key)
	if err != nil {
		t.Fatalf("SendMessage (replay): %v", err)
	}
	if !again.Replayed || again.ID != sent.ID {
		t.Errorf("replay = id %q replayed %v; want id %q replayed", again.ID, again.Replayed, sent.ID)
	}

	got, err := c.GetMessage(ctx, sent.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.ID != sent.ID || got.Status == "" || got.UpdatedAt.IsZero() {
		t.Errorf("GetMessage = id %q, status %q, updated %v", got.ID, got.Status, got.UpdatedAt)
	}
	if strings.Contains(string(got.Raw), token) {
		t.Error("the answer carries the token")
	}
}
