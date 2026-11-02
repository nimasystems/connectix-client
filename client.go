package connectix

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultTimeout bounds a whole request on the default HTTP client, so a
	// stalled connection can never hang a caller that forgot a context
	// deadline.
	DefaultTimeout = 15 * time.Second

	// maxBodyBytes caps how much of a response is read, so a broken or
	// hostile server cannot exhaust memory. A company's template list is the
	// largest real answer.
	maxBodyBytes = 4 << 20
)

// Client talks to the Connectix API with one application token. It is safe
// for concurrent use and holds no mutable state after [New] returns. Make it
// with New: the methods of a zero Client return an error.
type Client struct {
	token   *secret // a pointer: fmt prints a nested pointer as an address, never its target
	baseURL *url.URL
	http    *http.Client
	now     func() time.Time // the clock a Retry-After date is read against
}

// Option changes how [New] builds a [Client].
type Option func(*Client) error

// New returns a client that authenticates with token, the application's
// token from the Connectix console (the sandbox token for the sandbox host,
// the production token for the production host). A base URL is required:
// give [WithBaseURL].
//
// New refuses an empty token or one with whitespace, a missing or invalid
// base URL, and an invalid option. Its errors never include the token or
// the URL.
func New(token string, opts ...Option) (*Client, error) {
	if token == "" {
		return nil, errors.New("connectix: the API token is empty")
	}
	if strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return nil, errors.New("connectix: the API token contains whitespace or control characters")
	}
	c := &Client{
		token: &secret{value: token},
		http:  defaultHTTPClient(),
		now:   time.Now,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if c.baseURL == nil {
		return nil, errors.New("connectix: the base URL is missing; give WithBaseURL (there is no default, the sandbox and production tokens differ)")
	}
	return c, nil
}

// WithBaseURL points the client at an API root, such as
// https://api-sandbox.connectix.bg. It must be https, or http on a loopback
// host, and must not carry userinfo (user:password@), which could leak
// through logs. Its errors never echo the URL.
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseBaseURL(raw)
		if err != nil {
			return err
		}
		c.baseURL = u
		return nil
	}
}

// WithHTTPClient makes the client send through hc, for a caller that wants
// its own transport, proxy or instrumentation. hc is used as given: if its
// Timeout is zero, only the request context bounds a call, and it follows
// redirects unless its CheckRedirect says otherwise (the default client
// refuses them, because following one replays the POST).
//
// A transport error counts as [ErrOutcomeUnknown] unless it is clearly from
// before sending (dial, DNS, TLS handshake or certificate). A transport that
// wraps those errors in its own types, or reports them differently, makes
// more errors "unknown": the safe side.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc == nil {
			return errors.New("connectix: WithHTTPClient: the *http.Client is nil")
		}
		c.http = hc
		return nil
	}
}

// withNow replaces the clock, so a test reads a Retry-After date against a
// fixed time.
func withNow(now func() time.Time) Option {
	return func(c *Client) error {
		c.now = now
		return nil
	}
}

// String keeps the token out of anything that prints a client.
func (c Client) String() string {
	if c.baseURL == nil {
		return "connectix.Client{}"
	}
	return "connectix.Client{" + c.baseURL.Redacted() + "}"
}

// GoString keeps the token out of %#v.
func (c Client) GoString() string { return c.String() }

// Format prints String for every verb, so a verb String does not serve
// (%d, %t, %9.3f, ...) cannot make fmt walk the fields. %p of a Client
// value still walks them, which is why the token is behind a pointer.
func (c Client) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.String()) }

// LogValue keeps the token out of log/slog.
func (c Client) LogValue() slog.Value { return slog.StringValue(c.String()) }

// secret holds the token and refuses to print or encode it, with any verb.
// Only reveal returns the value, and only call uses it, for the
// Authorization header.
type secret struct{ value string }

const redacted = "[redacted]"

func (*secret) String() string               { return redacted }
func (*secret) GoString() string             { return redacted }
func (*secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (*secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (*secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (s *secret) reveal() string             { return s.value }

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("connectix: the base URL must be an absolute URL")
	}
	if u.User != nil {
		return nil, errors.New("connectix: the base URL must not carry credentials (userinfo); the token is the only credential")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return nil, errors.New("connectix: the base URL must use https (http only on loopback, for tests)")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// defaultHTTPClient does not follow redirects: the API never redirects, and
// following one would replay a POST, possibly with the Authorization header.
// A redirect comes back as an *APIError with its 3xx status.
func defaultHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Timeout:       DefaultTimeout,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// errNotBuilt is what a method returns on a Client that New did not make.
var errNotBuilt = errors.New("connectix: the Client was not made with New")

// ready reports whether c was made by New, so a zero or nil Client returns
// an error instead of panicking.
func (c *Client) ready() error {
	if c == nil || c.token == nil || c.baseURL == nil || c.http == nil || c.now == nil {
		return errNotBuilt
	}
	return nil
}

// request is one call: the method, the path under the base URL (no leading
// slash, already safe), an optional query, an optional idempotency key and
// an optional JSON body.
type request struct {
	method, path   string
	query          url.Values
	idempotencyKey string
	body           any
}

// answer is a successful response: its body exactly as read and its headers.
type answer struct {
	raw    json.RawMessage
	header http.Header
}

// call makes one request and decodes a successful JSON answer into out. A
// non-2xx answer is an *APIError. A 2xx body that does not decode is
// ErrOutcomeUnknown: the API answered, so it may have acted. The error's
// text never carries the token, the integration token in the query, or a
// body; only [APIError.Raw] holds a body.
func (c *Client) call(ctx context.Context, r request, out any) (*answer, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, fmt.Errorf("connectix: encode the %s request: %w", r.path, err)
		}
		body = bytes.NewReader(b)
	}
	target := c.baseURL.JoinPath(r.path)
	target.RawQuery = r.query.Encode()
	req, err := http.NewRequestWithContext(ctx, r.method, target.String(), body)
	if err != nil {
		return nil, errors.New("connectix: the request could not be built")
	}
	req.Header.Set("Authorization", c.token.reveal())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.idempotencyKey)
	}

	// A context already done means the request never leaves.
	if err := ctx.Err(); err != nil {
		return nil, &transportError{op: "request not sent", err: err}
	}
	// wrote turns true once the request headers are on the wire. It is only
	// ever a signal toward "unknown": a transport that drops the request
	// context never fires it, so its silence proves nothing.
	var wrote atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteHeaders: func() { wrote.Store(true) },
	}))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transportError{op: "request failed", err: stripURL(err), sent: wrote.Load() || !clearlyNotSent(err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, &transportError{op: "reading the answer failed", err: stripURL(err), sent: true}
	}
	if len(raw) > maxBodyBytes {
		return nil, &unexpectedError{reason: "the answer is larger than " + strconv.Itoa(maxBodyBytes) + " bytes"}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeAPIError(raw, resp.StatusCode, resp.Header, c.now())
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, &unexpectedError{reason: describeDecodeError(err)}
	}
	return &answer{raw: bytes.Clone(raw), header: resp.Header}, nil
}

// transportError is a request that got no HTTP answer. It unwraps to the
// cause (so context.Canceled and timeouts match errors.Is), and it is
// ErrOutcomeUnknown when the request had already been sent.
type transportError struct {
	op   string
	err  error
	sent bool
}

func (e *transportError) Error() string { return "connectix: " + e.op + ": " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }
func (e *transportError) Is(target error) bool {
	return target == ErrOutcomeUnknown && e.sent
}

// stripURL drops the *url.Error wrapper net/http adds, whose text carries
// the whole request URL, query included (the integration token is there).
// What is left is the cause, which still matches context.DeadlineExceeded,
// context.Canceled and net.Error.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// clearlyNotSent reports whether a transport error can only have happened
// before any of the request reached Connectix: the connection was never
// made (dial, DNS) or never became a trusted TLS session. Every other
// error, including ones a custom transport invents, may come after the
// request was sent, so the outcome is unknown.
func clearlyNotSent(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	var verify *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var authority x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &dns):
		return true
	case errors.As(err, &op) && op.Op == "dial":
		return true
	case errors.As(err, &verify), errors.As(err, &record),
		errors.As(err, &authority), errors.As(err, &host), errors.As(err, &invalid):
		return true
	}
	return false
}

// unexpectedError is a 2xx answer that could not be read. The request was
// answered, so Connectix may well have acted on it: it is ErrOutcomeUnknown.
type unexpectedError struct{ reason string }

func (e *unexpectedError) Error() string        { return "connectix: unexpected response: " + e.reason }
func (e *unexpectedError) Is(target error) bool { return target == ErrOutcomeUnknown }

// describeDecodeError says where a body failed to decode without quoting
// any of it: decoder errors can carry values from the body.
func describeDecodeError(err error) string {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return "invalid JSON at byte " + strconv.FormatInt(syntax.Offset, 10)
	case errors.As(err, &typ) && typ.Field != "":
		return "field " + typ.Field + " has the wrong type"
	case errors.As(err, &typ):
		return "the answer has the wrong type"
	default:
		return "the answer is not the expected JSON"
	}
}
