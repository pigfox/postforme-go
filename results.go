package postforme

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// listResultsOKCodes is the accepted success set for GET /social-post-results.
var listResultsOKCodes = []int{http.StatusOK}

// Result is one account's outcome for one post.
//
// It is the per-network half of a publish: a Post can be Processed while an
// individual network rejected it, so a consumer reconciling delivery reads
// these rather than the post's status.
//
// IT IS AN ALLOWLIST, and one omission is deliberate rather than incidental.
// The API's result carries a platform_data object that presumably holds the
// per-network post id and permalink — the fields a takedown flow would want.
// It is NOT decoded here, because no published post existed to observe its
// shape while this package was written, and inventing field names from a
// spec that declares the property as a bare object would be guessing at a
// contract. It is additive the moment a real result can be read.
type Result struct {
	// ID is the result's own identifier.
	ID string `json:"id"`
	// PostID is the post this result belongs to.
	PostID string `json:"post_id"`
	// AccountID is the social account this result is for.
	AccountID string `json:"social_account_id"`
	// Success reports whether the network accepted the post.
	Success bool `json:"success"`
	// Error is the vendor's failure detail, best-effort and often empty.
	//
	// The spec declares the error property as an untyped object, so this reads
	// the two scalar shapes a JSON error object almost always takes and yields
	// "" for anything else. It is never the raw body: resultError is a
	// two-field allowlist like every other decode here.
	Error string
}

// resultError is the narrow allowlist for a result's error object.
type resultError struct {
	Message string `json:"message"`
	Error   string `json:"error"`
}

func (r resultError) text() string {
	if r.Message != "" {
		return r.Message
	}
	return r.Error
}

// resultWire is the decode target. Result carries an unexported field, which
// encoding/json cannot populate, so the wire shape is decoded separately and
// projected. Doing it this way keeps Error a plain string on the public type
// instead of exposing the envelope.
type resultWire struct {
	ID        string      `json:"id"`
	PostID    string      `json:"post_id"`
	AccountID string      `json:"social_account_id"`
	Success   bool        `json:"success"`
	Error     resultError `json:"error"`
}

func (w resultWire) result() Result {
	return Result{
		ID:        w.ID,
		PostID:    w.PostID,
		AccountID: w.AccountID,
		Success:   w.Success,
		Error:     w.Error.text(),
	}
}

// ResultFilter narrows a ListResults call. A zero filter lists everything.
type ResultFilter struct {
	PostID    string
	AccountID string
	Platform  string
	Limit     int
	Offset    int
}

func (f ResultFilter) query() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{
		"post_id":           f.PostID,
		"social_account_id": f.AccountID,
		"platform":          f.Platform,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", strconv.Itoa(f.Offset))
	}
	return q
}

// ListResults returns one page of per-account publish outcomes.
func (c *Client) ListResults(ctx context.Context, f ResultFilter) ([]Result, Page, error) {
	var env listEnvelope[resultWire]
	err := c.do(ctx, request{
		method:  http.MethodGet,
		path:    "/social-post-results",
		query:   f.query(),
		out:     &env,
		op:      "listresults",
		okCodes: listResultsOKCodes,
	})
	if err != nil {
		return nil, Page{}, err
	}
	out := make([]Result, 0, len(env.Data))
	for _, w := range env.Data {
		out = append(out, w.result())
	}
	return out, env.page(), nil
}
