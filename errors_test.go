package postforme

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAPIErrorMessage(t *testing.T) {
	bare := &APIError{Op: "createpost", StatusCode: 500}
	if got, want := bare.Error(), "postforme createpost: http 500"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	withDetail := &APIError{Op: "createpost", StatusCode: 400, Validation: []string{"at least one social_account is required"}}
	if !strings.Contains(withDetail.Error(), "at least one social_account") {
		t.Errorf("Error() = %q, want the validation text", withDetail.Error())
	}
}

func TestStatusHelper(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"api error", &APIError{StatusCode: 404}, 404},
		{"wrapped api error", fmt.Errorf("outer: %w", &APIError{StatusCode: 429}), 429},
		{"plain error", errors.New("nope"), 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Status(c.err); got != c.want {
				t.Errorf("Status = %d, want %d", got, c.want)
			}
		})
	}
}

// TestClassificationTable is the contract a consumer's give-up logic reads.
//
// TERMINAL AND RETRYABLE ARE NOT COMPLEMENTS, and the table proves it rather
// than asserting it in a comment: a canceled context is neither, and a
// transport failure is retryable without being terminal's opposite. A consumer
// that treated !Terminal as "retry" would retry a canceled context forever,
// which is the unbounded-loop shape this whole client is meant to avoid.
func TestClassificationTable(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		terminal  bool
		retryable bool
		notFound  bool
	}{
		{"404 not found", &APIError{StatusCode: http.StatusNotFound}, true, false, true},
		{"400 bad request", &APIError{StatusCode: http.StatusBadRequest}, true, false, false},
		{"401 unauthorized", &APIError{StatusCode: http.StatusUnauthorized}, true, false, false},
		{"403 forbidden", &APIError{StatusCode: http.StatusForbidden}, true, false, false},
		{"422 unprocessable", &APIError{StatusCode: http.StatusUnprocessableEntity}, true, false, false},
		{"408 timeout", &APIError{StatusCode: http.StatusRequestTimeout}, false, true, false},
		{"409 conflict", &APIError{StatusCode: http.StatusConflict}, false, true, false},
		{"425 too early", &APIError{StatusCode: http.StatusTooEarly}, false, true, false},
		{"429 rate limited", &APIError{StatusCode: http.StatusTooManyRequests}, false, true, false},
		{"500 server error", &APIError{StatusCode: http.StatusInternalServerError}, false, true, false},
		{"503 unavailable", &APIError{StatusCode: http.StatusServiceUnavailable}, false, true, false},
		{"transport failure", errors.New("dial tcp: refused"), false, true, false},
		{"wrapped 404", fmt.Errorf("x: %w", &APIError{StatusCode: 404}), true, false, true},
		{"context canceled", context.Canceled, false, false, false},
		{"context deadline", context.DeadlineExceeded, false, false, false},
		{"wrapped cancellation", fmt.Errorf("x: %w", context.Canceled), false, false, false},
		{"nil", nil, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Terminal(c.err); got != c.terminal {
				t.Errorf("Terminal = %v, want %v", got, c.terminal)
			}
			if got := Retryable(c.err); got != c.retryable {
				t.Errorf("Retryable = %v, want %v", got, c.retryable)
			}
			if got := NotFound(c.err); got != c.notFound {
				t.Errorf("NotFound = %v, want %v", got, c.notFound)
			}
		})
	}
}

// TestValidationDetailIsOffByDefault is the default-posture guard: without the
// option, not one byte of an error body reaches the error value.
func TestValidationDetailIsOffByDefault(t *testing.T) {
	// The live 400 shape, verbatim.
	body := `{"statusCode":400,"message":"Invalid Request","errors":["at least one social_account is required"]}`

	off := newTestClient(t, staticJSON(400, body))
	_, err := off.CreatePost(t.Context(), CreatePostInput{Caption: "x", Accounts: []string{"a"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if len(apiErr.Validation) != 0 {
		t.Errorf("validation detail leaked with the option off: %v", apiErr.Validation)
	}
	for _, leak := range []string{"social_account", "Invalid Request"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the response body reached the error string: %v", err)
		}
	}
}

// TestValidationDetailReadsBothVendorShapes covers the documented shape and the
// one the API actually returns. They differ, and neither is a superset.
func TestValidationDetailReadsBothVendorShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"live shape: errors[]",
			`{"statusCode":400,"message":"Invalid Request","errors":["at least one social_account is required"]}`,
			"at least one social_account is required"},
		{"documented shape: error[]",
			`{"error":["caption must be a string"]}`,
			"caption must be a string"},
		{"message only",
			`{"statusCode":400,"message":"Invalid Request"}`,
			"Invalid Request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := newTestClient(t, staticJSON(400, c.body), WithValidationDetail())
			_, err := cl.CreatePost(t.Context(), CreatePostInput{Caption: "x", Accounts: []string{"a"}})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if len(apiErr.Validation) == 0 || apiErr.Validation[0] != c.want {
				t.Fatalf("Validation = %v, want [%q]", apiErr.Validation, c.want)
			}
		})
	}
}

// TestUnparseableErrorBodyIsNotGuessedAt: a body that is not JSON adds nothing,
// and the client must not invent detail or leak the bytes trying.
func TestUnparseableErrorBodyIsNotGuessedAt(t *testing.T) {
	c := newTestClient(t, staticJSON(500, `<html>gateway error NOT-A-REAL-TOKEN</html>`), WithValidationDetail())
	_, err := c.GetPost(t.Context(), "sp_1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if len(apiErr.Validation) != 0 {
		t.Errorf("Validation = %v, want empty for an unparseable body", apiErr.Validation)
	}
	if strings.Contains(err.Error(), "NOT-A-REAL-TOKEN") {
		t.Fatalf("an unparseable body leaked into the error: %v", err)
	}
}

// TestErrorNeverCarriesAnAccountPayload is the worst realistic case: an error
// response that happens to contain a full account object with tokens. Nothing
// from it may surface, with the option on or off.
func TestErrorNeverCarriesAnAccountPayload(t *testing.T) {
	body := `{"statusCode":403,"message":"forbidden","account":{"id":"spc_1",` +
		`"` + "access" + `_token":"NOT-A-REAL-TOKEN-xyz"}}`
	for _, detail := range []bool{false, true} {
		opts := []Option{}
		if detail {
			opts = append(opts, WithValidationDetail())
		}
		c := newTestClient(t, staticJSON(403, body), opts...)
		_, err := c.GetPost(t.Context(), "sp_1")
		if strings.Contains(err.Error(), "NOT-A-REAL-TOKEN") {
			t.Fatalf("validationDetail=%v: a token reached the error string: %v", detail, err)
		}
	}
}
