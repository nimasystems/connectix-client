// Package connectix is a small client for the Connectix messaging API
// (https://mcp.connectix.bg/openapi.json, version 1.3.0): it sends one
// message from an approved template, reads a message back, lists the
// templates and names the application behind the token.
//
// [Client.SendMessage] sends one SMS, Viber or Noti message to one number
// from a template and an optional idempotency key; [Client.GetMessage] reads
// a message with its current status; [Client.ListTemplates] lists the
// templates with the slots each one needs; [Client.Me] verifies a token and
// the host it is used against.
//
// [Client.RegisterIntegration], [Client.RegisterHook] and
// [Client.UnregisterHook] set up the webhooks that report a message's
// status, and [VerifyWebhook] and [ParseCallback] receive them: verify the
// raw body's signature first, then decode. The command cmd/connectix does the
// one-time set-up. The rest of the API (fallback flows, media messages,
// one-time passwords, the blacklist, TrustCheck, shipments) is not covered;
// the README keeps the list.
//
// There is no default base URL: the sandbox and the production host have
// different tokens, so [New] needs [WithBaseURL] and never guesses which
// one a token belongs to.
//
// A [Client] is safe for concurrent use. It holds the token behind an
// unexported pointer and never prints it: not in errors, not through fmt
// with any verb (%p and %d included), not through log/slog or
// encoding/json. It sends the token only in the Authorization header, and
// only over HTTPS (plain HTTP is allowed for a loopback base URL so tests
// can use net/http/httptest).
//
// Errors from the API are [*APIError] values. Callers branch on a class with
// errors.Is ([ErrUnauthorized], [ErrInsufficientFunds], [ErrRestricted],
// [ErrNotAcceptable], [ErrInProgress], [ErrValidation], [ErrRateLimited],
// [ErrNoContract], [ErrUnprocessable], [ErrTemplateNotFound]) or on the
// status with errors.As. [ErrOutcomeUnknown] marks a failure after which a
// message may or may not have been sent: retry [Client.SendMessage] only
// with the same idempotency key.
//
// The package uses the standard library only.
package connectix
