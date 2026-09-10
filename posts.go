package postforme

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Post status values the API returns.
const (
	PostDraft      = "draft"
	PostScheduled  = "scheduled"
	PostProcessing = "processing"
	PostProcessed  = "processed"
)

// createPostOKCodes is the accepted success set for POST /social-posts, and it
// holds TWO codes on purpose.
//
// THE DOCUMENTED CODE AND THE LIVE CODE DISAGREE. The OpenAPI document declares
// exactly {200, 400, 500} for this operation. A real create against the live
// API — a draft, made and deleted while writing this package — returned 201.
// Pinning either one alone is a bug waiting for the other: pin 200 and every
// successful publish today reads as a failure; pin 201 and the day the vendor
// makes its implementation match its spec, the same thing happens in reverse.
//
// This is not a hypothetical. The estate this was written for shipped a mail
// integration that compared a vendor's status against a single documented
// constant and spent a session on the mismatch. Accepting the documented code
// AND the observed code is the cheap fix, and the set is named so the next
// person sees both were a decision.
var createPostOKCodes = []int{http.StatusOK, http.StatusCreated}

// Accepted success sets for the remaining post operations, observed live.
var (
	getPostOKCodes    = []int{http.StatusOK}
	deletePostOKCodes = []int{http.StatusOK, http.StatusNoContent}
)

// ErrNoAccounts is returned by CreatePost when no account is targeted.
//
// It is checked client-side because the API's own answer is a 400 that costs a
// round-trip, and because a publish that targets nobody is always a caller bug
// — never a transient condition worth retrying.
var ErrNoAccounts = errors.New("postforme: a post must target at least one account")

// ErrNoCaption is returned by CreatePost when the caption is empty. caption is
// required by the API schema.
var ErrNoCaption = errors.New("postforme: a post must have a caption")

// CreatePostInput is a publish request.
type CreatePostInput struct {
	// Caption is the post text. Required.
	Caption string
	// Accounts are the social-account IDs to publish to. Required, non-empty.
	// They come from ListAccounts; this package hardcodes none, because which
	// accounts to publish to is a consumer's policy, not a client's knowledge.
	Accounts []string
	// ScheduledAt publishes at a future time. Zero means publish immediately.
	ScheduledAt time.Time
	// ExternalID is the caller's own reference for this post, echoed back on
	// reads. It is the natural place for a consumer's primary key or slug, and
	// it is what makes a publish idempotent to look up after a crash.
	ExternalID string
	// Draft creates the post without processing it. Useful for a smoke test
	// that must not reach an audience.
	Draft bool
}

// createPostBody is the wire shape. It is separate from CreatePostInput so the
// public type can use Go conventions (time.Time, no pointers) while the JSON
// stays exactly what the API accepts.
type createPostBody struct {
	Caption     string   `json:"caption"`
	Accounts    []string `json:"social_accounts"`
	ScheduledAt *string  `json:"scheduled_at,omitempty"`
	ExternalID  string   `json:"external_id,omitempty"`
	Draft       bool     `json:"isDraft,omitempty"`
}

func (in CreatePostInput) body() createPostBody {
	b := createPostBody{
		Caption:    in.Caption,
		Accounts:   in.Accounts,
		ExternalID: in.ExternalID,
		Draft:      in.Draft,
	}
	if !in.ScheduledAt.IsZero() {
		s := in.ScheduledAt.UTC().Format(time.RFC3339)
		b.ScheduledAt = &s
	}
	return b
}

// Post is a social post as the API reports it.
//
// IT IS AN ALLOWLIST. The response also carries platform_configurations,
// account_configurations and media, which are deliberately not decoded: this
// package covers text publishing, and inventing Go types for nested shapes no
// consumer needs yet would be guessing at a contract. They are additive when a
// consumer needs them.
type Post struct {
	// ID is the vendor's post identifier, used by GetPost and DeletePost.
	ID string `json:"id"`
	// ExternalID echoes CreatePostInput.ExternalID, empty if none was set.
	ExternalID string `json:"external_id"`
	// Caption is the post text.
	Caption string `json:"caption"`
	// Status is one of the Post* constants.
	Status string `json:"status"`
	// Accounts are the social-account IDs this post targets.
	Accounts []string `json:"social_accounts"`
	// CreatedAt and UpdatedAt are the vendor's timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Processed reports whether the vendor has finished handling this post.
// It says nothing about whether each network accepted it — that is what
// ListResults answers, per account.
func (p Post) Processed() bool { return p.Status == PostProcessed }

// CreatePost publishes a post, or schedules or drafts one.
func (c *Client) CreatePost(ctx context.Context, in CreatePostInput) (Post, error) {
	if in.Caption == "" {
		return Post{}, ErrNoCaption
	}
	if len(in.Accounts) == 0 {
		return Post{}, ErrNoAccounts
	}
	var out Post
	err := c.do(ctx, request{
		method:  http.MethodPost,
		path:    "/social-posts",
		body:    in.body(),
		out:     &out,
		op:      "createpost",
		okCodes: createPostOKCodes,
	})
	if err != nil {
		return Post{}, err
	}
	return out, nil
}

// GetPost fetches one post by its vendor ID.
//
// A 404 here is the vendor stating the post does not exist, which NotFound
// reports and which a reconcile loop should treat as terminal rather than
// retry — see the Terminal and Retryable helpers.
func (c *Client) GetPost(ctx context.Context, id string) (Post, error) {
	if id == "" {
		return Post{}, ErrEmptyID
	}
	var out Post
	err := c.do(ctx, request{
		method:  http.MethodGet,
		path:    "/social-posts/" + url.PathEscape(id),
		out:     &out,
		op:      "getpost",
		okCodes: getPostOKCodes,
	})
	if err != nil {
		return Post{}, err
	}
	return out, nil
}

// DeletePost removes a post. Deleting an already-deleted post returns a 404,
// which NotFound reports; most callers can treat that as success.
func (c *Client) DeletePost(ctx context.Context, id string) error {
	if id == "" {
		return ErrEmptyID
	}
	return c.do(ctx, request{
		method:  http.MethodDelete,
		path:    "/social-posts/" + url.PathEscape(id),
		op:      "deletepost",
		okCodes: deletePostOKCodes,
	})
}

// ErrEmptyID is returned when an operation needing an ID is given none. It is
// caught client-side because an empty ID builds a URL for the collection
// endpoint, where a DELETE would mean something entirely different from what
// the caller asked for.
var ErrEmptyID = errors.New("postforme: an id is required")
