// Package postforme is a small, dependency-free client for the Post for Me
// social publishing API (https://api.postforme.dev).
//
// # Scope
//
// This module is the HTTP client and nothing else. It knows about requests,
// responses, errors and retries. It knows nothing about databases, job queues,
// scheduling, content composition, or which accounts you publish to — those are
// the consumer's concerns, and different consumers answer them differently.
// Anything of theirs that leaked in here would stop this being reusable.
//
// # Security
//
// The API returns OAuth credentials on its social-account resource:
// access_token and refresh_token are documented fields of SocialAccountDto and
// are present in live responses. THIS PACKAGE CANNOT REPRESENT THEM. Account
// declares no token field, so a token has nowhere to be decoded into, is never
// held beyond the read buffer, and cannot reach a log line or an error string.
// TestNoCredentialFieldsOnDecodedTypes walks this package with go/ast and fails
// if a JSON-tagged credential field is ever added.
//
// The official Stainless-generated library takes the opposite approach — it
// declares AccessToken and RefreshToken on its SocialAccount and exposes
// RawJSON() on every response struct — which is why this package exists rather
// than wrapping it. A wrapper cannot un-declare a field or un-retain a body.
package postforme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production API root. The version prefix is part of it.
//
// THE PREFIX IS "/v1" AND THAT IS NOT COSMETIC. Post for Me's marketing page
// documents the posting endpoint as /social-posts; the OpenAPI document served
// at /docs declares every path under /v1. The spec is authoritative and the
// marketing page is wrong — a client built from the marketing page 404s.
const DefaultBaseURL = "https://api.postforme.dev/v1"

// Client tunables. Each is overridable through an Option and none is read from
// the environment: a library that reads the environment decides something that
// belongs to its caller.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 2
	DefaultBackoff    = 250 * time.Millisecond

	// maxBackoff caps exponential growth so a retry chain cannot wait minutes.
	maxBackoff = 30 * time.Second

	// maxResponseBytes bounds a response read. The body is decoded and dropped.
	maxResponseBytes = 4 << 20
)

// Client talks to the Post for Me API.
//
// There is no package-level instance and no init(): construct one with New and
// pass it. The zero value is not usable.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	maxRetries int
	backoff    time.Duration

	// sleep is a seam so retry tests spend no wall-clock time. Production uses a
	// timer that also honors context cancellation.
	sleep func(ctx context.Context, d time.Duration) error

	// validationDetail opts in to carrying the vendor's validation strings on an
	// APIError. Off by default — see WithValidationDetail.
	validationDetail bool
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root, for a proxy or a test server. The value
// is used verbatim after trailing slashes are trimmed, so include any prefix.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient supplies the underlying client, for callers that pool
// connections, set transport timeouts, or inject a fake transport.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithMaxRetries sets how many times a retryable failure is retried. Zero
// disables retrying; negative values clamp to zero.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// WithBackoff sets the base delay for exponential backoff between retries.
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff = d }
}

// WithValidationDetail lets an APIError carry the vendor's validation messages
// for a 4xx response.
//
// IT IS OFF BY DEFAULT, DELIBERATELY. Every response this package decodes is
// filtered through an allowlist struct and the raw body is never retained, and
// the error envelope is no exception — it decodes into three scalar fields.
// Even so, those strings originate in a response body, and this package's
// default posture is that nothing from a body reaches an error string unless
// the caller asks for it in writing. Turn it on when you want a 400 to say what
// was wrong with the request; leave it off when errors are logged somewhere you
// do not fully control.
func WithValidationDetail() Option {
	return func(c *Client) { c.validationDetail = true }
}

// ErrNoAPIKey is returned by New when the key is empty or blank.
//
// It is an error rather than a silent empty-bearer client because the estate
// that prompted this module spent a session on a mail integration that
// presented an empty credential to a vendor on every call and reported nothing.
var ErrNoAPIKey = errors.New("postforme: an API key is required")

// New builds a Client. The API key is the only required input.
func New(apiKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, ErrNoAPIKey
	}
	c := &Client{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: DefaultTimeout},
		maxRetries: DefaultMaxRetries,
		backoff:    DefaultBackoff,
		sleep:      sleepCtx,
	}
	for _, o := range opts {
		o(c)
	}
	// WithHTTPClient(nil) is reachable and must not produce a client that
	// panics on first use. There is deliberately NO matching nil-check for
	// sleep: no Option can reach it, so a guard there would be a branch no test
	// could ever take, and unreachable defensive code reads as protection while
	// providing none.
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return c, nil
}

// sleepCtx waits for d, returning early if ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// request describes one call. Grouping the parameters keeps do's signature
// readable rather than a run of eight positional arguments.
type request struct {
	method  string
	path    string
	query   url.Values
	body    any
	out     any // nil when the caller wants only the status
	op      string
	okCodes []int
}

// do performs one API call with retries, decoding a success body into r.out.
//
// THE BODY IS READ, DECODED AND DROPPED. It is never stored on the Client,
// never attached to an error, and never handed back as bytes. r.out is always a
// narrow struct whose fields are the allowlist: encoding/json discards every
// key with no matching field, so a token in the payload has nowhere to land.
func (c *Client) do(ctx context.Context, r request) error {
	var payload []byte
	if r.body != nil {
		var err error
		if payload, err = json.Marshal(r.body); err != nil {
			return fmt.Errorf("postforme %s: encode request: %w", r.op, err)
		}
	}

	endpoint := c.baseURL + r.path
	if len(r.query) > 0 {
		endpoint += "?" + r.query.Encode()
	}

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoffFor(c.backoff, attempt)); err != nil {
				return fmt.Errorf("postforme %s: %w", r.op, err)
			}
		}
		err := c.attempt(ctx, endpoint, payload, r)
		if err == nil {
			return nil
		}
		if attempt >= c.maxRetries || !Retryable(err) {
			return err
		}
	}
}

// attempt performs a single round-trip.
func (c *Client) attempt(ctx context.Context, endpoint string, payload []byte, r request) error {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, endpoint, rdr)
	if err != nil {
		return fmt.Errorf("postforme %s: build request: %w", r.op, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("postforme %s: %w", r.op, err)
	}
	//nolint:errcheck // close-on-read-path: the body is fully read below and a close error
	// cannot change the outcome; this package holds no logger to report it to.
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("postforme %s: read response (http %d): %w", r.op, resp.StatusCode, err)
	}

	if !statusIn(resp.StatusCode, r.okCodes) {
		return c.apiError(r.op, resp.StatusCode, raw)
	}
	if r.out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, r.out); err != nil {
		// TYPED, NOT fmt.Errorf, AND THAT IS THE WHOLE POINT. The status is a
		// success: the API performed the operation and only the reading of its
		// answer failed. A plain error here is indistinguishable from a dial
		// failure, so Retryable classified it as transport, do() retried it
		// three times, the caller's queue retried that, and one post was
		// published six times. *DecodeError is refused by Retryable and
		// accepted by Terminal.
		//
		// The body is deliberately NOT carried. The bytes that failed to parse
		// are exactly the thing that may hold a credential — a create response
		// embeds OAuth tokens for every targeted account.
		return &DecodeError{Op: r.op, StatusCode: resp.StatusCode, Err: err}
	}
	return nil
}

// backoffFor returns the delay before the given 1-based attempt.
func backoffFor(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for range attempt - 1 {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// statusIn reports whether code is one of the accepted successes.
func statusIn(code int, ok []int) bool {
	for _, c := range ok {
		if c == code {
			return true
		}
	}
	return false
}
