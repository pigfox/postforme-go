package postforme

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// Account status values the API returns.
const (
	AccountConnected    = "connected"
	AccountDisconnected = "disconnected"
)

// listAccountsOKCodes is the accepted success set for GET /social-accounts.
var listAccountsOKCodes = []int{http.StatusOK}

// Account is a connected social account.
//
// IT IS AN ALLOWLIST, AND THE OMISSIONS ARE THE FEATURE. The API's
// SocialAccountDto also carries access_token, refresh_token,
// access_token_expires_at and refresh_token_expires_at — verified present in a
// live response, field names read back from the payload. None of them appears
// here, so encoding/json drops them at the boundary: they are not redacted
// after the fact, they are never materialized. There is no accessor, no
// String() that could leak one, and no raw body kept anywhere for someone to
// reach into later.
//
// If you need a token, you are holding the wrong library. Tokens belong to the
// vendor's OAuth flow, not to a publishing client.
type Account struct {
	// ID is the account identifier used in CreatePostInput.Accounts.
	ID string `json:"id"`
	// Platform is the network, e.g. "linkedin", "facebook", "bluesky".
	Platform string `json:"platform"`
	// Username is the human-readable handle or page name.
	Username string `json:"username"`
	// Status is AccountConnected or AccountDisconnected.
	Status string `json:"status"`
	// ExternalID is the caller's own identifier for this account, when one was
	// set at connect time. Empty otherwise.
	ExternalID string `json:"external_id"`
}

// Connected reports whether the account can currently be published to.
func (a Account) Connected() bool { return a.Status == AccountConnected }

// AccountFilter narrows a ListAccounts call. A zero filter lists everything.
type AccountFilter struct {
	Platform   string
	Username   string
	Status     string
	ExternalID string
	// Limit and Offset page the result. Limit <= 0 uses the API default.
	Limit  int
	Offset int
}

func (f AccountFilter) query() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{
		"platform":    f.Platform,
		"username":    f.Username,
		"status":      f.Status,
		"external_id": f.ExternalID,
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

// Page is the pagination envelope the list endpoints return.
type Page struct {
	// Total is how many records match, across all pages.
	Total int `json:"total"`
	// Offset and Limit echo the request.
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
	// HasMore reports whether another page exists.
	HasMore bool `json:"-"`
}

// listEnvelope is the allowlist for a paginated response.
type listEnvelope[T any] struct {
	Data []T `json:"data"`
	Meta struct {
		Total  int     `json:"total"`
		Offset int     `json:"offset"`
		Limit  int     `json:"limit"`
		Next   *string `json:"next"`
	} `json:"meta"`
}

func (e listEnvelope[T]) page() Page {
	return Page{
		Total:   e.Meta.Total,
		Offset:  e.Meta.Offset,
		Limit:   e.Meta.Limit,
		HasMore: e.Meta.Next != nil && *e.Meta.Next != "",
	}
}

// ListAccounts returns one page of connected social accounts.
//
// It is the reason a consumer should not hardcode account IDs: an ID changes
// when an account is reconnected, and a hardcoded one goes stale silently —
// the publish succeeds against an account nobody is watching, or fails with a
// vendor error that reads like an outage.
func (c *Client) ListAccounts(ctx context.Context, f AccountFilter) ([]Account, Page, error) {
	var env listEnvelope[Account]
	err := c.do(ctx, request{
		method:  http.MethodGet,
		path:    "/social-accounts",
		query:   f.query(),
		out:     &env,
		op:      "listaccounts",
		okCodes: listAccountsOKCodes,
	})
	if err != nil {
		return nil, Page{}, err
	}
	return env.Data, env.page(), nil
}
