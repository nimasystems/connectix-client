# connectix-client

A small Go client for the [Connectix](https://connectix.bg/) messaging API:
send one message from an approved template, read it back, list the templates
and check a token.

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
| Integrations and hooks (`/integration/*`) and the webhooks | no |

## Contributing

Read [CLAUDE.md](CLAUDE.md): it holds the rules for every change. `make check`
is the gate.

## License

[MIT](LICENSE).
