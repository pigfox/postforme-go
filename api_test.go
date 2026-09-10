package postforme

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// --- posts ---------------------------------------------------------------------

// TestCreatePostAcceptsBothSuccessCodes is the regression for the one defect
// this package was most likely to ship with.
//
// The OpenAPI document declares 200 for POST /social-posts. The live API
// returns 201 — observed against a real draft while writing this. A client
// pinned to either single value breaks on the other, and the failure mode is
// the worst kind: every successful publish is reported as a failure, so a
// consumer retries something that already happened.
func TestCreatePostAcceptsBothSuccessCodes(t *testing.T) {
	for _, code := range createPostOKCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := newTestClient(t, staticJSON(code, `{"id":"sp_1","status":"processed","caption":"hi"}`))
			p, err := c.CreatePost(t.Context(), CreatePostInput{Caption: "hi", Accounts: []string{"spc_a"}})
			if err != nil {
				t.Fatalf("http %d must be a success: %v", code, err)
			}
			if p.ID != "sp_1" {
				t.Errorf("ID = %q", p.ID)
			}
		})
	}
	if len(createPostOKCodes) < 2 {
		t.Fatal("the accepted set must hold BOTH the documented 200 and the observed 201; " +
			"narrowing it to one reintroduces the vendor-docs mismatch")
	}
}

func TestCreatePostValidatesLocally(t *testing.T) {
	// A transport that fails the test if it is ever reached: these rejections
	// must cost no round-trip.
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Error("a locally-invalid request must not reach the network")
		return jsonResponse(200, `{}`), nil
	})
	if _, err := c.CreatePost(t.Context(), CreatePostInput{Accounts: []string{"a"}}); !errors.Is(err, ErrNoCaption) {
		t.Errorf("err = %v, want ErrNoCaption", err)
	}
	if _, err := c.CreatePost(t.Context(), CreatePostInput{Caption: "x"}); !errors.Is(err, ErrNoAccounts) {
		t.Errorf("err = %v, want ErrNoAccounts", err)
	}
}

func TestCreatePostScheduledAndDraft(t *testing.T) {
	var body string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		body = string(b)
		return jsonResponse(201, `{"id":"sp_1","status":"draft"}`), nil
	})
	when := time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)
	p, err := c.CreatePost(t.Context(), CreatePostInput{
		Caption: "later", Accounts: []string{"spc_a"}, ScheduledAt: when, Draft: true,
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if p.Status != PostDraft || p.Processed() {
		t.Errorf("status = %q, Processed = %v", p.Status, p.Processed())
	}
	for _, want := range []string{`"scheduled_at":"2026-09-15T12:30:00Z"`, `"isDraft":true`} {
		if !contains(body, want) {
			t.Errorf("body missing %s\ngot %s", want, body)
		}
	}
}

func TestGetPost(t *testing.T) {
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/social-posts/sp_abc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		return jsonResponse(200, `{"id":"sp_abc","external_id":"blog:x","caption":"c",
			"status":"processed","social_accounts":["spc_a"],
			"created_at":"2026-09-10T01:00:00Z","updated_at":"2026-09-10T02:00:00Z"}`), nil
	})
	p, err := c.GetPost(t.Context(), "sp_abc")
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	switch {
	case p.ID != "sp_abc":
		t.Errorf("ID = %q", p.ID)
	case p.ExternalID != "blog:x":
		t.Errorf("ExternalID = %q", p.ExternalID)
	case !p.Processed():
		t.Error("Processed() should be true for status=processed")
	case len(p.Accounts) != 1 || p.Accounts[0] != "spc_a":
		t.Errorf("Accounts = %v", p.Accounts)
	case !p.CreatedAt.Equal(time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)):
		t.Errorf("CreatedAt = %v", p.CreatedAt)
	}
}

func TestDeletePost(t *testing.T) {
	for _, code := range deletePostOKCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodDelete {
					t.Errorf("method = %s, want DELETE", r.Method)
				}
				return jsonResponse(code, ``), nil
			})
			if err := c.DeletePost(t.Context(), "sp_1"); err != nil {
				t.Fatalf("DeletePost: %v", err)
			}
		})
	}
}

// TestEmptyIDIsRejectedLocally: an empty id builds the COLLECTION url, where a
// DELETE means something entirely different from what the caller asked for.
func TestEmptyIDIsRejectedLocally(t *testing.T) {
	c := newTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Error("an empty id must not reach the network")
		return jsonResponse(200, `{}`), nil
	})
	if _, err := c.GetPost(t.Context(), ""); !errors.Is(err, ErrEmptyID) {
		t.Errorf("GetPost err = %v, want ErrEmptyID", err)
	}
	if err := c.DeletePost(t.Context(), ""); !errors.Is(err, ErrEmptyID) {
		t.Errorf("DeletePost err = %v, want ErrEmptyID", err)
	}
}

func TestPostIDIsPathEscaped(t *testing.T) {
	var path string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		path = r.URL.EscapedPath()
		return jsonResponse(200, `{"id":"x"}`), nil
	})
	if _, err := c.GetPost(t.Context(), "a/../b"); err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if contains(path, "/../") {
		t.Fatalf("path traversal survived escaping: %s", path)
	}
}

// --- accounts --------------------------------------------------------------------

func TestListAccountsFilterAndPaging(t *testing.T) {
	var query string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		query = r.URL.RawQuery
		return jsonResponse(200, `{"data":[
			{"id":"spc_1","platform":"linkedin","username":"A","status":"connected","external_id":null},
			{"id":"spc_2","platform":"facebook","username":"B","status":"disconnected","external_id":"ext"}
		],"meta":{"total":9,"offset":2,"limit":2,"next":"more"}}`), nil
	})
	accts, page, err := c.ListAccounts(t.Context(), AccountFilter{
		Platform: "linkedin", Username: "A", Status: AccountConnected,
		ExternalID: "ext", Limit: 2, Offset: 2,
	})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	for _, want := range []string{"platform=linkedin", "username=A", "status=connected", "external_id=ext", "limit=2", "offset=2"} {
		if !contains(query, want) {
			t.Errorf("query missing %s: %s", want, query)
		}
	}
	if len(accts) != 2 {
		t.Fatalf("got %d accounts", len(accts))
	}
	if accts[0].Connected() == accts[1].Connected() {
		t.Error("the two fixtures differ in status; Connected() should too")
	}
	if page.Total != 9 || page.Offset != 2 || page.Limit != 2 || !page.HasMore {
		t.Errorf("page = %+v", page)
	}
}

func TestListAccountsEmptyFilterSendsNoQuery(t *testing.T) {
	var query string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		query = r.URL.RawQuery
		return jsonResponse(200, `{"data":[],"meta":{"total":0,"offset":0,"limit":50,"next":null}}`), nil
	})
	_, page, err := c.ListAccounts(t.Context(), AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if query != "" {
		t.Errorf("a zero filter should send no query, got %q", query)
	}
	if page.HasMore {
		t.Error("HasMore should be false when next is null")
	}
}

func TestListAccountsPropagatesAnError(t *testing.T) {
	c := newTestClient(t, staticJSON(500, `{}`))
	_, _, err := c.ListAccounts(t.Context(), AccountFilter{})
	if Status(err) != 500 {
		t.Fatalf("err = %v, want 500", err)
	}
}

// --- results ----------------------------------------------------------------------

func TestListResults(t *testing.T) {
	var query string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		query = r.URL.RawQuery
		return jsonResponse(200, `{"data":[
			{"id":"r1","post_id":"sp_1","social_account_id":"spc_a","success":true,"error":{}},
			{"id":"r2","post_id":"sp_1","social_account_id":"spc_b","success":false,"error":{"message":"token expired"}},
			{"id":"r3","post_id":"sp_1","social_account_id":"spc_c","success":false,"error":{"error":"rejected"}}
		],"meta":{"total":3,"offset":0,"limit":50,"next":null}}`), nil
	})
	res, page, err := c.ListResults(t.Context(), ResultFilter{
		PostID: "sp_1", AccountID: "spc_a", Platform: "linkedin", Limit: 10, Offset: 5,
	})
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	for _, want := range []string{"post_id=sp_1", "social_account_id=spc_a", "platform=linkedin", "limit=10", "offset=5"} {
		if !contains(query, want) {
			t.Errorf("query missing %s: %s", want, query)
		}
	}
	if len(res) != 3 || page.Total != 3 {
		t.Fatalf("got %d results, page %+v", len(res), page)
	}
	switch {
	case !res[0].Success || res[0].Error != "":
		t.Errorf("r1 = %+v, want success with no error text", res[0])
	case res[1].Error != "token expired":
		t.Errorf("r2 error = %q, want the message field", res[1].Error)
	case res[2].Error != "rejected":
		t.Errorf("r3 error = %q, want the error field when message is absent", res[2].Error)
	}
}

func TestListResultsEmptyFilter(t *testing.T) {
	var query string
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		query = r.URL.RawQuery
		return jsonResponse(200, `{"data":[],"meta":{"total":0,"offset":0,"limit":50,"next":null}}`), nil
	})
	if _, _, err := c.ListResults(t.Context(), ResultFilter{}); err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if query != "" {
		t.Errorf("a zero filter should send no query, got %q", query)
	}
}

func TestListResultsPropagatesAnError(t *testing.T) {
	c := newTestClient(t, staticJSON(503, `{}`))
	_, _, err := c.ListResults(t.Context(), ResultFilter{})
	if !Retryable(err) {
		t.Fatalf("err = %v, want a retryable 503", err)
	}
}

// contains is strings.Contains, named locally so the assertions above read as
// prose rather than as a wall of strings.Contains calls.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
