package postforme_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	postforme "github.com/pigfox/postforme-go"
)

// Example shows the whole publish path: discover accounts, publish, then read
// the per-network outcomes.
//
// THE KEY COMES FROM THE ENVIRONMENT AND IS NEVER PRINTED. Nothing in this
// repository contains a credential, including this example.
func Example() {
	client, err := postforme.New(os.Getenv("POSTFORME_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// Discover accounts rather than hardcoding IDs: an ID changes when an
	// account is reconnected, and a stale hardcoded one fails in a way that
	// reads like an outage.
	accounts, _, err := client.ListAccounts(ctx, postforme.AccountFilter{
		Platform: "linkedin",
		Status:   postforme.AccountConnected,
	})
	if err != nil {
		log.Fatal(err)
	}
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}

	post, err := client.CreatePost(ctx, postforme.CreatePostInput{
		Caption:    "New post: how we verify vendor status codes.\n\nhttps://example.com/blog/post",
		Accounts:   ids,
		ExternalID: "blog:how-we-verify-vendor-status-codes",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(post.Status)

	// Per-network outcomes. A post can be processed while one network rejected
	// it, so delivery is read here rather than from the post's status.
	results, _, err := client.ListResults(ctx, postforme.ResultFilter{PostID: post.ID})
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range results {
		fmt.Printf("%s success=%v %s\n", r.AccountID, r.Success, r.Error)
	}
}

// ExampleTerminal shows the classification a retry loop needs.
//
// Terminal and Retryable are not complements — a canceled context is neither —
// so ask the question you mean rather than negating the other one.
func ExampleTerminal() {
	client, err := postforme.New("key-from-your-environment")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.GetPost(ctx, "sp_does_not_exist")
	switch {
	case err == nil:
		fmt.Println("found")
	case postforme.NotFound(err):
		// The vendor states the post does not exist. No later attempt can make
		// it appear: stop asking, record why, and alert once.
		fmt.Println("gone for good")
	case postforme.Terminal(err):
		fmt.Println("the request itself is wrong; retrying repeats the mistake")
	case postforme.Retryable(err):
		fmt.Println("transient; try again with backoff")
	default:
		// Neither terminal nor retryable: a canceled or expired context.
		fmt.Println("giving up:", errors.Unwrap(err))
	}
}
