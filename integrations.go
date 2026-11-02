package connectix

import (
	"context"
	"errors"
	"net/http"
)

// integrationSecretLen is the exact length Connectix demands of the secret
// that signs an integration's webhooks.
const integrationSecretLen = 32

// integrationKinds are the kinds of system an integration can be.
var integrationKinds = []string{"custom", "ecommerce", "automation"}

// hookTypes are the events a hook can subscribe to. Only "callback" has a
// parser here ([ParseCallback]); the others are verified with [VerifyWebhook]
// and their bodies are the caller's to decode.
var hookTypes = []string{"callback", "inbound", "template", "tracking", "blacklist"}

func contains(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// RegisterIntegration registers (or re-activates) an integration of the
// application and returns its token: pass it as
// [SendMessageRequest.Integration] so the application's hooks receive the
// message's statuses, and to [Client.RegisterHook]. kind is one of
// custom, ecommerce or automation, platform a free-text name of the system,
// and secret the caller's own 32 printable ASCII bytes, which Connectix keeps
// and signs every webhook delivery with ([VerifyWebhook]). Registering again with the
// same secret returns the same integration and token, so a lost token is
// recovered by repeating the call.
//
// The token and the secret are credentials. Keep both out of logs; this
// method never puts either in an error. This acts on the application's real
// data on the sandbox host too.
func (c *Client) RegisterIntegration(ctx context.Context, kind, platform string, secret []byte) (token string, err error) {
	if !contains(integrationKinds, kind) {
		return "", errors.New("connectix: the integration kind must be custom, ecommerce or automation")
	}
	if platform == "" {
		return "", errors.New("connectix: the integration platform is empty")
	}
	if len(secret) != integrationSecretLen {
		return "", errors.New("connectix: the integration secret must be exactly 32 bytes")
	}
	// Connectix counts characters and the secret travels in JSON: only
	// printable ASCII keeps bytes, characters and the signing key the same.
	for _, b := range secret {
		if b < 0x21 || b > 0x7e {
			return "", errors.New("connectix: the integration secret must be printable ASCII without spaces")
		}
	}
	body := struct {
		Type     string `json:"type"`
		Platform string `json:"platform"`
		Secret   string `json:"secret"`
	}{kind, platform, string(secret)}
	var out struct {
		Token string `json:"token"`
	}
	if _, err := c.call(ctx, request{method: http.MethodPost, path: "integration/register", body: body}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", &unexpectedError{reason: "the answer has no token"}
	}
	return out.Token, nil
}

// RegisterHook subscribes url to one event type of an integration, with the
// integration token from [Client.RegisterIntegration], and returns the
// hook's id for [Client.UnregisterHook]. hookType is one of callback, inbound, template, tracking or blacklist; url
// must be publicly reachable, or Connectix answers [ErrValidation]. Register
// one hook per type. This acts on the application's real data on the
// sandbox host too.
func (c *Client) RegisterHook(ctx context.Context, integrationToken, hookType, url string) (id string, err error) {
	if integrationToken == "" {
		return "", errors.New("connectix: the integration token is empty")
	}
	if !contains(hookTypes, hookType) {
		return "", errors.New("connectix: the hook type must be callback, inbound, template, tracking or blacklist")
	}
	if url == "" {
		return "", errors.New("connectix: the hook URL is empty")
	}
	body := struct {
		Token string `json:"token"`
		Type  string `json:"type"`
		URL   string `json:"url"`
	}{integrationToken, hookType, url}
	var out struct {
		ID string `json:"id"`
	}
	if _, err := c.call(ctx, request{method: http.MethodPost, path: "integration/register/hook", body: body}, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", &unexpectedError{reason: "the answer has no hook id"}
	}
	return out.ID, nil
}

// UnregisterHook removes a hook by the id [Client.RegisterHook] returned. A
// hook that does not exist, or is not the application's, is an [*APIError]
// with status 404.
func (c *Client) UnregisterHook(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("connectix: the hook id is empty")
	}
	var out string
	if _, err := c.call(ctx, request{method: http.MethodPost, path: "integration/unregister/hook", body: struct {
		ID string `json:"id"`
	}{id}}, &out); err != nil {
		return err
	}
	if out != "ok" {
		return &unexpectedError{reason: "the answer is not ok"}
	}
	return nil
}
