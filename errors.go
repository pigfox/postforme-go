package postforme

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// APIError is a non-success HTTP response from the API.
//
// IT CARRIES THE STATUS CODE AS A FIELD, and that is the point of the type
// rather than a detail of it. A consumer deciding whether a failed publish is
// terminal or worth retrying needs to tell a 404 from a 503, and an error whose
// only content is a formatted string forces that decision to be made by string
// matching — which then breaks silently the day the vendor rewords a message.
// Terminal and Retryable read the status; nothing needs to read the text.
type APIError struct {
	// Op is the client operation that failed, e.g. "createpost".
	Op string
	// StatusCode is the HTTP status the API returned.
	StatusCode int
	// Validation carries the vendor's validation messages for a 4xx, and is
	// populated ONLY when the client was built WithValidationDetail. It is
	// empty otherwise, by default and by design.
	Validation []string
}

func (e *APIError) Error() string {
	if len(e.Validation) == 0 {
		return fmt.Sprintf("postforme %s: http %d", e.Op, e.StatusCode)
	}
	return fmt.Sprintf("postforme %s: http %d: %v", e.Op, e.StatusCode, e.Validation)
}

// DecodeError is a SUCCESS response whose body could not be decoded.
//
// # It is a distinct type because the right response to it is the opposite one
//
// Every other failure in this package means the operation did not happen. This
// one means the API accepted the request — it answered 2xx — and only the
// reading of its answer failed. For a GET that is a nuisance. For CreatePost it
// is the difference between "publish it again" and "you have already published
// it", and getting that backwards publishes the same post to every connected
// network once per attempt.
//
// THAT IS NOT HYPOTHETICAL. v0.2.0 declared Post.Accounts as []string against a
// live API that returns objects, so every create decoded-failed after a 201.
// The error was a plain fmt.Errorf, Retryable saw something that was not an
// *APIError and classified it as a transport failure, and one blog announcement
// went out six times across four networks before the retry chain was stopped by
// hand. Five of those could not be deleted: the vendor refuses to remove a
// processed post.
//
// So this type exists, Retryable returns FALSE for it, Terminal returns TRUE for
// it, and the message says in words that the operation may have succeeded.
type DecodeError struct {
	// Op is the client operation, e.g. "createpost".
	Op string
	// StatusCode is the SUCCESS status the API returned before the body failed
	// to decode. It is always one of the operation's accepted codes.
	StatusCode int
	// Err is the underlying decode failure.
	//
	// THE BODY IS NOT HERE AND MUST NOT BE ADDED. The bytes that failed to
	// parse are exactly the bytes that may carry a credential — the create
	// response embeds OAuth tokens for every targeted account — so the one
	// thing this type will not tell you is what it could not read.
	Err error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("postforme %s: http %d succeeded but the response could not be decoded, "+
		"so THE OPERATION MAY HAVE BEEN PERFORMED — do not retry it blindly: %v",
		e.Op, e.StatusCode, e.Err)
}

// Unwrap exposes the underlying decode failure.
func (e *DecodeError) Unwrap() error { return e.Err }

// Undecoded reports whether err is a 2xx whose body could not be read.
//
// It is the predicate a caller needs before deciding to retry a non-idempotent
// operation, and it is spelled out rather than left to errors.As at every call
// site for the same reason NotFound is.
func Undecoded(err error) bool {
	var de *DecodeError
	return errors.As(err, &de)
}

// Status returns the HTTP status code, or 0 if err is not an *APIError. It
// saves every caller writing the same errors.As dance.
func Status(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// NotFound reports whether err is the API answering 404.
//
// It exists because "the resource does not exist" is the one vendor answer that
// is terminal on sight: no later attempt can make a missing post appear, so a
// consumer's reconcile loop should stop asking rather than retry forever.
func NotFound(err error) bool {
	return Status(err) == http.StatusNotFound
}

// Terminal reports whether err is a failure that retrying cannot fix: a 4xx
// that is not one of the transient four (408, 409, 425, 429).
//
// Terminal and Retryable are NOT complements. A context cancellation is
// neither, and a transport error is retryable without being an APIError at all,
// so a consumer that treats !Terminal as "retry" will retry a canceled context
// forever. Ask the question you mean.
func Terminal(err error) bool {
	// A 2xx THAT COULD NOT BE DECODED IS TERMINAL, and it is the one terminal
	// condition that is not a 4xx. The request succeeded; repeating it would
	// perform the operation a second time. Callers ask Terminal precisely to
	// decide whether to stop, so answering "not terminal" here is what turned a
	// decode bug into six published copies of one post.
	if Undecoded(err) {
		return true
	}
	code := Status(err)
	if code < 400 || code > 499 {
		return false
	}
	return !transient4xx(code)
}

// Retryable reports whether err is worth another attempt: a transport failure,
// a transient 4xx, or any 5xx. A canceled or expired context is never
// retryable, however it is wrapped.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// A DECODED-FAILED SUCCESS IS NEVER RETRYABLE, and this check must come
	// BEFORE the not-an-APIError fallthrough below — which is exactly where it
	// used to land, and why it was retried. The operation may already have been
	// performed; a retry is a second one.
	if Undecoded(err) {
		return false
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		// Not an API response at all: a dial failure, a TLS error, a truncated
		// read. Those are the canonical retryable conditions.
		return true
	}
	if apiErr.StatusCode >= http.StatusInternalServerError {
		return true
	}
	return transient4xx(apiErr.StatusCode)
}

// transient4xx is the retryable client-error set, named rather than inlined so
// the membership is one decision in one place.
//
// 429 is rate limiting, 408 is a request timeout, 409 is a conflict that a
// later attempt may win, and 425 is "too early". Every other 4xx says the
// request itself is wrong, and repeating a wrong request is how a client turns
// its own bug into vendor load.
func transient4xx(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict,
		http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// errorEnvelope is the allowlist for a non-2xx body.
//
// TWO SHAPES ARE ACCEPTED BECAUSE THE VENDOR SHIPS TWO. The OpenAPI document
// declares InvalidSocialPostDto as {"error": ["..."]}; the live API answers a
// bad create with {"statusCode":400,"message":"Invalid Request","errors":["at
// least one social_account is required"]}. Neither key is documented as the
// other, so both are read and whichever is present wins. Everything else in the
// body is discarded by encoding/json, which is what makes this an allowlist
// rather than a parse.
type errorEnvelope struct {
	Message string   `json:"message"`
	Errors  []string `json:"errors"`
	Error   []string `json:"error"`
}

// apiError builds the typed error for a non-success response.
//
// raw is READ AND DROPPED. When validation detail is off — the default — not
// one byte of it reaches the returned value.
func (c *Client) apiError(op string, status int, raw []byte) error {
	e := &APIError{Op: op, StatusCode: status}
	if !c.validationDetail {
		return e
	}
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return e // an unparseable error body adds nothing and is not guessed at
	}
	switch {
	case len(env.Errors) > 0:
		e.Validation = env.Errors
	case len(env.Error) > 0:
		e.Validation = env.Error
	case env.Message != "":
		e.Validation = []string{env.Message}
	}
	return e
}
