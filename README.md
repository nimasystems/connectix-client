# connectix-client

A small Go client for the [Connectix](https://connectix.bg/) messaging API:
send one message from an approved template, read it back, list the templates,
check a token, register an integration and its webhook hooks, and verify the
signed deliveries those hooks receive. A small command, `connectix`, does the
one-time set-up.

It was written for sending phone-verification codes by SMS from a Go backend,
and it covers that job well rather than the whole API. It is built to grow:
each new operation is added the same way, with tests (see
[Contributing](#contributing)).

- Standard library only: no dependencies.
- Go 1.24 or later: the oldest release it needs (its tests use
  `testing.T.Context`, new in 1.24). CI tests 1.24 and the latest stable Go.
- Safe for concurrent use.
- The token never appears in errors, in `fmt` output with any verb, or in
  `log/slog`.
- Written against the API document version 1.2.0, kept in
  [`testdata/openapi.json`](testdata/openapi.json).

> **Not affiliated with Connectix.** This is an independent, open-source
> client. "Connectix" is named only to say which API it talks to.

## Install

```sh
go get github.com/nimasystems/connectix-client@latest
```

```go
import connectix "github.com/nimasystems/connectix-client"
```

## Quick start

```go
client, err := connectix.New(os.Getenv("CONNECTIX_TOKEN"),
	connectix.WithBaseURL("https://api-sandbox.connectix.bg"),
)
if err != nil {
	return err
}

msg, err := client.SendMessage(ctx, connectix.SendMessageRequest{
	Phone:      "+359881234567",
	Template:   "3644d7ac-2a95-4068-863e-6352ed78d667",
	Parameters: map[string]string{"code": "123456"},
}, idempotencyKey) // a fresh UUID per logical send
if err != nil {
	return err
}
log.Printf("accepted message %s (%s)", msg.ID, msg.Status)
```

Each Connectix application has two tokens, one for the sandbox host
(`https://api-sandbox.connectix.bg`) and one for the production host. They
are not interchangeable: a wrong or mismatched token is a 401. So the
library has **no default base URL**; `New` without `WithBaseURL` is an
error, and the caller says which host it means. On the sandbox, delivery is
simulated and nothing is billed.

## Configuration

`New(token, opts...)` returns a `*Client`. It refuses an empty token, a token
with whitespace, a missing base URL, and an invalid option. Always make a
client with `New`: the methods of a zero `Client{}` return an error.

| Option | Default | Notes |
|---|---|---|
| `WithBaseURL(url)` | none: required | `https`, or `http` on loopback for tests; no userinfo |
| `WithHTTPClient(hc)` | 15 s timeout, TLS 1.2+, no redirects | Used as given; set its `Timeout` and `CheckRedirect` yourself |

## Sending and reading

| Method | Operation |
|---|---|
| `SendMessage(ctx, req, idempotencyKey)` | `POST /messages` |
| `GetMessage(ctx, id)` | `GET /messages/{id}` |
| `ListTemplates(ctx)` | `GET /templates` |
| `Me(ctx)` | `GET /me` |

- `SendMessageRequest.Integration` goes to the query `integration`; the other
  fields go to the JSON body under Connectix's names. Empty fields are left
  out.
- A non-empty idempotency key is sent as `Idempotency-Key`. A repeat with the
  same key returns the stored answer with `Message.Replayed` set and sends
  nothing a second time.
- `Message.RateLimitRemaining` is the `X-RateLimit-Remaining` header, `-1`
  when absent. `Message.Raw` is the answer exactly as received.
- `Template.Slots` lists the `{{ name }}` variables in the template text,
  once each, in order of first appearance; every one needs a value in
  `Parameters`.

## Integrations and signed webhooks

An integration groups the messages one system sends and owns the webhook
hooks that report on them. Set it up once, then send with its token so the
hooks hear of each message's status.

| Method | Operation |
|---|---|
| `RegisterIntegration(ctx, kind, platform, secret)` | `POST /integration/register`; returns the integration token |
| `RegisterHook(ctx, integrationToken, hookType, url)` | `POST /integration/register/hook`; returns the hook's id |
| `UnregisterHook(ctx, id)` | `POST /integration/unregister/hook` |

- `kind` is `custom`, `ecommerce` or `automation`; `secret` is **your own 32
  bytes of printable ASCII** (no spaces), which Connectix keeps and signs
  every delivery with. Registering again with the same secret returns the same
  integration and token, so a lost token is recovered by repeating the call.
- `hookType` is `callback` (delivery status), `inbound`, `template`,
  `tracking` or `blacklist`; register one hook per type. The URL must be
  public: Connectix refuses a private, loopback or link-local address with a
  422 (`ErrValidation`).
- The integration token and the secret are credentials. Neither is put in an
  error. Pass the token as `SendMessageRequest.Integration`.

Connectix signs a delivery to an integration hook with the headers
`X-Connectix-Timestamp` (Unix seconds) and `X-Connectix-Signature` (`v1=` and
the hex HMAC-SHA256 of `"<timestamp>.<raw body>"` keyed with the secret).
Verify the **raw** body before you decode it:

```go
body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
err := connectix.VerifyWebhook(secret, // the 32 bytes chosen at registration
	r.Header.Get("X-Connectix-Timestamp"),
	r.Header.Get("X-Connectix-Signature"),
	body, time.Now())
if err != nil { // ErrBadSignature or ErrStaleTimestamp
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return
}
cb, err := connectix.ParseCallback(body) // a "callback" hook's body
// answer 2xx at once, then deduplicate on cb.EventID and order by cb.CreatedAt
```

`VerifyWebhook` compares in constant time, accepts a timestamp within 5
minutes of `now` either way, and checks the signature first. It does not stop
a replay inside the window: delivery is at-least-once anyway, so drop repeats
on `Callback.EventID`. Deliveries to a per-message `callbackUrl` are not
signed; validate their `id` against your own records.

## The set-up command

```sh
go install github.com/nimasystems/connectix-client/cmd/connectix@latest

export CONNECTIX_TOKEN=...                      # the application's token for the host
export CONNECTIX_BASE_URL=https://api-sandbox.connectix.bg

connectix me                                    # the application's name
connectix templates                             # id, name, channel, status, slots; never the text
printf '%s' "$SECRET32" | connectix register-integration --platform "My system" --out integration.token
connectix register-hook --url https://example.com/hooks/connectix < integration.token   # prints the hook's id
connectix unregister-hook --id <hook id>
```

- Secrets are never arguments (a process list shows those). The secret comes
  on standard input; the integration token is written to the `--out` file
  (mode 0600, refused if it exists) and never printed; `register-hook` reads
  it from standard input and prints only the hook's id.
- Exit codes: 0 done, 1 the call failed (the message names the HTTP status,
  never the body), 2 refused before calling.
- **The sandbox is not a sandbox for these.** The sandbox host takes its own
  token, but the integration and hook calls act on the application's real
  data, which the sandbox shares with production. Register once, on purpose.

## Errors

A non-2xx answer is an `*APIError` with `Status`, `Message` (Connectix's
reason or its `error` code), `RetryAfter` and `Raw`. Its `Error()` reads
`connectix: HTTP <status>` and nothing more, because the reason can quote the
phone number. Branch on a class with `errors.Is`:

| Sentinel | Status | Meaning |
|---|---|---|
| `ErrUnauthorized` | 401 | wrong token, or the other host's token |
| `ErrInsufficientFunds` | 402 | the balance cannot cover the message |
| `ErrRestricted` | 403 | staff disabled the resource for the application |
| `ErrNotAcceptable` | 406 | traffic suspended, or no sender ID for the country |
| `ErrInProgress` | 409 | the idempotency key is still being processed; wait `RetryAfter` and repeat |
| `ErrValidation` | 422 | a field was refused; `Raw` maps each field to its reason |
| `ErrRateLimited` | 429 | the minute's budget is spent; wait `RetryAfter` |
| `ErrNoContract` | 451 | no signed contract for the template's channel |
| `ErrUnprocessable` | 502 | unparsable number, country not enabled, no provider |
| `ErrTemplateNotFound` | 400 | the message is "The template was not found." |

`ErrOutcomeUnknown` marks a failure after which the message may or may not
have been sent: a timeout or a dropped connection after the request left, or a
2xx answer that could not be read. Repeat the send only with the same
idempotency key. A failure that cannot have reached Connectix (a refused
dial, DNS, a TLS handshake, a context already done) is not this.

## Coverage of the Connectix API

| Operation | Covered |
|---|---|
| `sendMessage` (`POST /messages`) | yes |
| `getMessage` (`GET /messages/{id}`) | yes |
| `listTemplates` (`GET /templates`) | yes |
| `getMe` (`GET /me`) | yes |
| `listMessages` (`GET /messages`) | no |
| `sendFallbackMessage`, `sendMediaMessage` | no |
| One-time passwords (`/otp/*`) | no |
| Blacklist, TrustCheck (`/blacklist*`, `/shopper/check`) | no |
| Shipments (`/shipments`) | no |
| `registerIntegration` (`POST /integration/register`) | yes |
| `registerIntegrationHook`, `unregisterIntegrationHook` (`POST /integration/register/hook`, `/integration/unregister/hook`) | yes |
| `unregisterIntegration` (`POST /integration/unregister`) | no |
| Webhook signature (`x-webhooks`) and the `callback` payload | yes: `VerifyWebhook`, `ParseCallback` |
| The `inbound`, `template`, `tracking` and `blacklist` payloads | no: verify with `VerifyWebhook`, decode yourself |

## Contributing

Read [CLAUDE.md](CLAUDE.md): it holds the rules for every change. `make check`
is the gate.

## License

[MIT](LICENSE).
