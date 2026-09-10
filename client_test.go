package postforme

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- fake transport ----------------------------------------------------------
//
// NO TEST IN THIS PACKAGE REACHES THE NETWORK. Every one drives a
// roundTripFunc, so the suite is hermetic, runs offline, and cannot be made to
// spend a vendor call by a stray edit. There are no credentials anywhere in
// these files: the client is constructed with an obvious placeholder.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testAPIKey is a placeholder, never a real credential.
const testAPIKey = "test-key-not-a-credential"

// jsonResponse builds a response with a JSON body.
func jsonResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// staticJSON always answers with the same status and body.
func staticJSON(code int, body string) roundTripFunc {
	return func(*http.Request) (*http.Response, error) { return jsonResponse(code, body), nil }
}

// newTestClient builds a client over a fake transport, with retries off and an
// instant sleep so no test spends wall-clock time.
func newTestClient(t *testing.T, rt roundTripFunc, opts ...Option) *Client {
	t.Helper()
	base := []Option{
		WithHTTPClient(&http.Client{Transport: rt}),
		WithBaseURL("https://api.test/v1"),
		WithMaxRetries(0),
	}
	c, err := New(testAPIKey, append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// --- construction --------------------------------------------------------------

func TestNewRejectsAnEmptyKey(t *testing.T) {
	for _, key := range []string{"", "   ", "\t\n"} {
		if _, err := New(key); !errors.Is(err, ErrNoAPIKey) {
			t.Errorf("New(%q) error = %v, want ErrNoAPIKey — an empty bearer authenticates nothing "+
				"and a client that accepts one fails silently against the vendor", key, err)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := New(testAPIKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	switch {
	case c.baseURL != DefaultBaseURL:
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	case c.maxRetries != DefaultMaxRetries:
		t.Errorf("maxRetries = %d, want %d", c.maxRetries, DefaultMaxRetries)
	case c.backoff != DefaultBackoff:
		t.Errorf("backoff = %v, want %v", c.backoff, DefaultBackoff)
	case c.httpClient == nil || c.sleep == nil:
		t.Error("New left a nil collaborator")
	case c.validationDetail:
		t.Error("validation detail must be OFF by default")
	}
}

// TestDefaultBaseURLCarriesTheVersionPrefix pins the one fact a client built
// from the vendor's marketing page would get wrong.
func TestDefaultBaseURLCarriesTheVersionPrefix(t *testing.T) {
	if !strings.HasSuffix(DefaultBaseURL, "/v1") {
		t.Fatalf("DefaultBaseURL = %q, want a /v1 suffix. The OpenAPI document declares every path "+
			"under /v1; the marketing page omits it and a client built from that 404s.", DefaultBaseURL)
	}
}

func TestOptions(t *testing.T) {
	custom := &http.Client{}
	c, err := New(testAPIKey,
		WithBaseURL("https://proxy.test/v1/"),
		WithHTTPClient(custom),
		WithMaxRetries(7),
		WithBackoff(time.Second),
		WithValidationDetail(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	switch {
	case c.baseURL != "https://proxy.test/v1":
		t.Errorf("baseURL = %q, want the trailing slash trimmed", c.baseURL)
	case c.httpClient != custom:
		t.Error("WithHTTPClient did not take")
	case c.maxRetries != 7:
		t.Errorf("maxRetries = %d, want 7", c.maxRetries)
	case c.backoff != time.Second:
		t.Errorf("backoff = %v, want 1s", c.backoff)
	case !c.validationDetail:
		t.Error("WithValidationDetail did not take")
	}

	neg, err := New(testAPIKey, WithMaxRetries(-3), WithHTTPClient(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if neg.maxRetries != 0 {
		t.Errorf("negative retries = %d, want clamped to 0", neg.maxRetries)
	}
	if neg.httpClient == nil {
		t.Error("a nil http client must fall back to a working default")
	}
}

// --- the request the client actually sends ---------------------------------------

func TestRequestShape(t *testing.T) {
	var got *http.Request
	var body string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			body = string(b)
		}
		return jsonResponse(201, `{"id":"sp_1","status":"processed"}`), nil
	})

	_, err := c.CreatePost(t.Context(), CreatePostInput{
		Caption:    "hello",
		Accounts:   []string{"spc_a", "spc_b"},
		ExternalID: "blog:some-slug",
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}

	switch {
	case got.Method != http.MethodPost:
		t.Errorf("method = %s, want POST", got.Method)
	case got.URL.String() != "https://api.test/v1/social-posts":
		t.Errorf("url = %s", got.URL)
	case got.Header.Get("Authorization") != "Bearer "+testAPIKey:
		t.Errorf("Authorization header = %q", got.Header.Get("Authorization"))
	case got.Header.Get("Content-Type") != "application/json":
		t.Errorf("Content-Type = %q", got.Header.Get("Content-Type"))
	case got.Header.Get("Accept") != "application/json":
		t.Errorf("Accept = %q", got.Header.Get("Accept"))
	}

	for _, want := range []string{`"caption":"hello"`, `"social_accounts":["spc_a","spc_b"]`, `"external_id":"blog:some-slug"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s\ngot: %s", want, body)
		}
	}
	// Omitted optionals must not appear at all, rather than as nulls the API
	// would have to interpret.
	for _, absent := range []string{"scheduled_at", "isDraft"} {
		if strings.Contains(body, absent) {
			t.Errorf("request body should omit %s when unset\ngot: %s", absent, body)
		}
	}
}

func TestContextIsHonoured(t *testing.T) {
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return jsonResponse(200, `{}`), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GetPost(ctx, "sp_1"); err == nil {
		t.Fatal("a canceled context must fail the call")
	}
}

// --- backoff -----------------------------------------------------------------------

func TestBackoffFor(t *testing.T) {
	base := 100 * time.Millisecond
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, base}, // clamped up to attempt 1
		{1, base},
		{2, 2 * base},
		{3, 4 * base},
		{4, 8 * base},
		{20, maxBackoff}, // capped, not overflowed
	}
	for _, c := range cases {
		if got := backoffFor(base, c.attempt); got != c.want {
			t.Errorf("backoffFor(%v, %d) = %v, want %v", base, c.attempt, got, c.want)
		}
	}
	if got := backoffFor(time.Hour, 3); got != maxBackoff {
		t.Errorf("a large base must still cap at %v, got %v", maxBackoff, got)
	}
}

func TestStatusIn(t *testing.T) {
	if !statusIn(201, []int{200, 201}) {
		t.Error("201 should be in {200,201}")
	}
	if statusIn(404, []int{200, 201}) {
		t.Error("404 should not be in {200,201}")
	}
	if statusIn(200, nil) {
		t.Error("nothing is in an empty accepted set")
	}
}

// --- retries -------------------------------------------------------------------------

func TestRetriesUntilSuccess(t *testing.T) {
	var calls int
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return jsonResponse(503, `{}`), nil
		}
		return jsonResponse(200, `{"id":"sp_1"}`), nil
	}, WithMaxRetries(3))
	c.sleep = func(context.Context, time.Duration) error { return nil }

	if _, err := c.GetPost(t.Context(), "sp_1"); err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if calls != 3 {
		t.Errorf("made %d attempts, want 3", calls)
	}
}

func TestRetriesGiveUpAndReturnTheLastError(t *testing.T) {
	var calls int
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(500, `{}`), nil
	}, WithMaxRetries(2))
	c.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := c.GetPost(t.Context(), "sp_1")
	if Status(err) != 500 {
		t.Fatalf("err = %v, want an APIError carrying 500", err)
	}
	if calls != 3 { // the initial attempt plus two retries
		t.Errorf("made %d attempts, want 3 (1 + 2 retries)", calls)
	}
}

// TestTerminalIsNotRetried is the guard PF-S334's give-up logic depends on: a
// consumer must be able to tell a permanent refusal from a transient one, and
// the client must not spend attempts on the permanent kind.
func TestTerminalIsNotRetried(t *testing.T) {
	var calls int
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(404, `{}`), nil
	}, WithMaxRetries(5))
	c.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := c.GetPost(t.Context(), "sp_gone")
	if !NotFound(err) {
		t.Fatalf("err = %v, want NotFound", err)
	}
	if calls != 1 {
		t.Errorf("a 404 was attempted %d times; it must be tried exactly once", calls)
	}
}

func TestRetryStopsOnContextCancellation(t *testing.T) {
	var calls int
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(503, `{}`), nil
	}, WithMaxRetries(5))
	c.sleep = func(context.Context, time.Duration) error { return context.Canceled }

	_, err := c.GetPost(t.Context(), "sp_1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("made %d attempts; the backoff wait should have aborted the chain after 1", calls)
	}
}

func TestTransportErrorIsRetried(t *testing.T) {
	var calls int
	wantErr := errors.New("dial tcp: connection refused")
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return nil, wantErr
	}, WithMaxRetries(2))
	c.sleep = func(context.Context, time.Duration) error { return nil }

	if _, err := c.GetPost(t.Context(), "sp_1"); err == nil {
		t.Fatal("want an error")
	}
	if calls != 3 {
		t.Errorf("made %d attempts, want 3", calls)
	}
}

// --- decode failures ---------------------------------------------------------------------

// TestDecodeFailureDoesNotLeakTheBody is a security assertion wearing a
// robustness hat: the bytes that failed to parse are exactly the bytes that
// might carry a credential, so they must not reach the error string.
func TestDecodeFailureDoesNotLeakTheBody(t *testing.T) {
	secret := "NOT-A-REAL-TOKEN-abcdef"
	c := newTestClient(t, staticJSON(200, `{"id": `+strconv.Quote(secret)+`, this is not json`))

	_, err := c.GetPost(t.Context(), "sp_1")
	if err == nil {
		t.Fatal("want a decode error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the response body reached the error string: %v", err)
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("the error should say what failed, got %v", err)
	}
}

func TestEncodeFailureIsReported(t *testing.T) {
	c := newTestClient(t, staticJSON(200, `{}`))
	// A channel cannot be marshaled; routed through the generic do so the
	// encode branch is exercised without a public API that permits it.
	err := c.do(t.Context(), request{
		method:  http.MethodPost,
		path:    "/x",
		body:    make(chan int),
		op:      "probe",
		okCodes: []int{200},
	})
	if err == nil || !strings.Contains(err.Error(), "encode request") {
		t.Fatalf("err = %v, want an encode-request failure", err)
	}
}

func TestBuildRequestFailureIsReported(t *testing.T) {
	c := newTestClient(t, staticJSON(200, `{}`))
	c.baseURL = "://not a url"
	err := c.do(t.Context(), request{method: http.MethodGet, path: "/x", op: "probe", okCodes: []int{200}})
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("err = %v, want a build-request failure", err)
	}
}

func TestReadFailureIsReported(t *testing.T) {
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{}), Header: http.Header{}}, nil
	})
	_, err := c.GetPost(t.Context(), "sp_1")
	if err == nil || !strings.Contains(err.Error(), "read response") {
		t.Fatalf("err = %v, want a read-response failure", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
