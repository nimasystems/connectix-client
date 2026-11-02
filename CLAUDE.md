# CLAUDE.md: rules for every change

These rules bind people and coding agents alike. The README says what the
library does; this file says how to change it.

## The gate

- `make check` is the gate: gofmt, `go vet`, staticcheck, govulncheck,
  `go test -race` and a 90% coverage floor (`make cover`). CI
  (`.github/workflows/ci.yml`) runs the tests on the oldest supported Go
  (the `go` directive in `go.mod`, 1.24) and on the latest stable, and the
  rest on the latest stable.
- Keep the `go` directive at the oldest release the code and tests need, and
  the README's stated minimum and the CI matrix equal to it.
- Run `make check` before you push. A red gate is never merged.
- Never weaken the gate to make a change pass: no lowering the coverage
  floor, no skipping a check, no `//nolint`-style escapes. Fix the change.
- Every Makefile target has a `## description` (`make help` lists them).

## Tests first

- TDD: write the failing test, watch it fail for the right reason, make it
  pass, then refactor. A check you never saw fail is not trusted. When a test
  passes at once, break the code on purpose to see it fail.
- Standard `testing` only. No assertion libraries, no mock generators. Fakes
  are `httptest` servers that assert the exact method, path, headers, query
  and JSON body.
- Name tests as sentences: `TestASendPostsTheTemplateWithTheIdempotencyKeyAndTheIntegration`.
- Cover the error paths of every endpoint: each status Connectix documents
  (a plain string, an object with `error`, a 422 field map, a 409 with
  `retry_after`), a non-JSON body (an HTML page from a proxy), an empty body,
  malformed JSON on a 2xx, context cancel and timeout.
- No real network in tests, and no real token anywhere: tests use the
  placeholder `testToken`. The one exception is `sandbox_test.go`, which
  talks to the Connectix sandbox only when `CONNECTIX_SANDBOX_TOKEN` is set
  (and `CONNECTIX_SANDBOX_BASE_URL` names another host) and is skipped
  otherwise, so CI needs no secret. Run it locally with the token in the
  environment; never print, log or commit it.
- The sandbox shares the company's real application data for templates. The
  sandbox test calls `Me`, `ListTemplates`, `SendMessage` and `GetMessage`
  only: it delivers and bills nothing.
- Inject the clock: code that compares against "now" uses `Client.now`
  (`withNow` in tests), never `time.Now` directly, so tests run on a fixed
  time.

## Dependencies

- Standard library only. A new dependency needs a reason in the pull
  request (what it does that the standard library cannot do reasonably) and
  the owner's agreement. Tools run through `go run pkg@version` in the
  Makefile do not count, because they never enter `go.mod`.

## Security and personal data

- Never log, print or put in an error the API token. It lives behind an
  unexported pointer (`token *secret`): `fmt` prints a nested pointer as an
  address, so even a verb `String` does not serve (`%d`, `%p` of a `Client`
  value) cannot reach it. `secret` and `Client` also redact through `Format`,
  `String`, `GoString`, `LogValue` and `MarshalText`.
  `TestTheClientNeverPrintsItsTokenWithAnyVerb` checks every verb; extend it
  when you add a field. `Client` has no exported token field. Keep it that
  way. Only `call` reveals the token, for the `Authorization` header (the
  raw token, as Connectix documents it). It never goes in a URL or a query
  string.
- The `integration` token is a credential too, and it travels in the query.
  A transport error would print the URL with it, so `call` strips the
  `*url.Error` wrapper; keep it that way.
- `APIError.Error()` reads `connectix: HTTP <status>` and nothing else.
  Connectix's reason can quote what the caller sent (a phone number), so it
  lives in `Message` and `Raw` alone, documented as personal data.
- Errors never echo phone numbers, template parameters (a one-time code),
  message text, URLs with credentials or raw response bodies. Name the rule
  instead.
- The base URL is `https`, or `http` only on loopback for tests, and never
  carries userinfo. There is no default base URL: the sandbox and the
  production host take different tokens, and a client must say which one it
  talks to.

## Adding an endpoint

1. Read the operation in `testdata/openapi.json` (the API document, version
   1.2.0, from https://mcp.connectix.bg/openapi.json) and note any doubt (an
   undocumented field, a conflicting limit) in the pull request and in the
   README. Refresh the file when Connectix publishes a new version, and say
   so in the commit.
2. Write the tests first, in `<feature>_test.go`: the exact request (method,
   path, headers, query, body), the success answer, and the error paths
   above.
3. Implement in `<feature>.go` as a method on `*Client` that takes a
   `context.Context` and goes through `c.call`, so auth, limits and error
   decoding stay in one place. Validate input locally only where a bad value
   would change the request itself (a path segment, a header).
4. Keep money a string. `Price` is a decimal string; no float holds money.
5. Add any new error class as an `Err*` sentinel and match it in
   `APIError.Is`.
6. Document every exported identifier, saying why it exists, not what it
   is.
7. Update the README: the usage section and the **Coverage of the Connectix
   API** table. The table must stay true in every commit.

## Compatibility

- The module is `v0` while the API settles. Still, prefer adding over
  changing: a new optional field or option rather than a changed signature.
  A breaking change gets a `!` in the commit type and a line in the release
  notes.
- Releases and tags are made by the owner only.

## Git

- Conventional Commits: `type(scope)!: lowercase description`, at most
  100 characters (`feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`).
  Branches are `<type>/<description>`. Pull requests are squash-merged, so
  the title becomes the commit. Dependabot titles its pull requests
  "ci: Bump ..." / "chore(deps): Bump ..."; lowercase the title when you
  squash.
- Never commit a token, a `.env` file or a captured response that contains
  real phone numbers.
