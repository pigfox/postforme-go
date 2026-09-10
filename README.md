# postforme-go

A small, dependency-free Go client for the [Post for Me](https://www.postforme.dev)
social publishing API.

[![CI](https://github.com/pigfox/postforme-go/actions/workflows/ci.yml/badge.svg)](https://github.com/pigfox/postforme-go/actions/workflows/ci.yml)

```
go get github.com/pigfox/postforme-go
```

Requires Go 1.24+. **No third-party dependencies** — standard library only.

## Why this exists

Post for Me ships an official, Stainless-generated Go library. Use it if it
suits you. This one exists because of two properties that library has and this
one deliberately does not:

- Its `SocialAccount` declares `AccessToken` and `RefreshToken` fields, so a
  vendor OAuth token is decoded into your process on every account listing.
- Every response struct exposes `RawJSON()`, so the complete response body —
  tokens included — is retained and one method call from any log line.

Neither is fixable by wrapping: a wrapper cannot un-declare a field or
un-retain a body. See [Security](#security).

## Usage

```go
client, err := postforme.New(os.Getenv("POSTFORME_API_KEY"))
if err != nil {
    return err
}

// Discover accounts rather than hardcoding IDs — an ID changes when an account
// is reconnected, and a stale one fails in a way that reads like an outage.
accounts, _, err := client.ListAccounts(ctx, postforme.AccountFilter{
    Status: postforme.AccountConnected,
})
if err != nil {
    return err
}

ids := make([]string, 0, len(accounts))
for _, a := range accounts {
    ids = append(ids, a.ID)
}

post, err := client.CreatePost(ctx, postforme.CreatePostInput{
    Caption:    "New post: https://example.com/blog/thing",
    Accounts:   ids,
    ExternalID: "blog:thing", // your own reference, echoed back on reads
})
```

Per-network delivery is separate from the post's own status — a post can be
`processed` while one network rejected it:

```go
results, _, err := client.ListResults(ctx, postforme.ResultFilter{PostID: post.ID})
for _, r := range results {
    if !r.Success {
        log.Printf("%s rejected the post: %s", r.AccountID, r.Error)
    }
}
```

## Error classification

Errors carry the HTTP status as a field, so a retry loop never has to match on
message text:

```go
switch {
case postforme.NotFound(err):  // 404 — the post does not exist; stop asking
case postforme.Terminal(err):  // 4xx that retrying cannot fix
case postforme.Retryable(err): // transport failure, 408/409/425/429, or any 5xx
}
```

**`Terminal` and `Retryable` are not complements.** A canceled context is
neither. A loop that treats `!Terminal` as "retry" will retry a canceled
context forever — ask the question you mean.

Retries are built in (2 by default, exponential backoff capped at 30s) and are
applied only to retryable failures. `WithMaxRetries(0)` turns them off.

## Security

**The API returns OAuth credentials on its account resource.** `access_token`
and `refresh_token` are documented fields of `SocialAccountDto` and are present
in live responses.

This package cannot represent them:

- `Account` declares no token field. `encoding/json` discards keys with no
  matching field, so a token has nowhere to be decoded into. It is not redacted
  after the fact — it is never materialised.
- No response body is retained. Bodies are read, decoded into an allowlist
  struct, and dropped. There is no `RawJSON()` here and no way to add one
  without failing the guard below.
- No response body reaches an error string. `APIError` carries the operation
  and the status code, nothing else, unless you explicitly opt in with
  `WithValidationDetail()` — which is off by default and decodes only three
  scalar fields when on.

`TestNoCredentialFieldsOnDecodedTypes` walks this package with `go/ast` and
fails if a JSON-tagged credential field is ever added, in any casing. It carries
its own sharpness test, and a runtime reflection check runs beside it because
neither subsumes the other.

## A note on the vendor's status codes

The OpenAPI document declares **200** for `POST /v1/social-posts`. The live API
returns **201**. Both are accepted (`createPostOKCodes`), because pinning either
one alone breaks on the other — and the failure mode is that every successful
publish reads as a failure and gets retried.

Likewise, the vendor's marketing page documents the endpoint as `/social-posts`;
the spec puts every path under `/v1`. `DefaultBaseURL` includes the prefix.

The documented error body is `{"error": ["..."]}`; the live one is
`{"statusCode":400,"message":"...","errors":["..."]}`. Both shapes are read.

## Scope

In: HTTP client, auth, typed requests and responses, typed errors, retries with
backoff, context handling.

Out, deliberately: job queues, databases, schemas, scheduling policy, content
composition, and account IDs. Those belong to consumers, and three different
consumers answer them three different ways.

Also out for now, and additive when a consumer needs them: media upload,
platform-specific configuration blocks, webhook management, and the
`platform_data` field on a result (its shape was not observable at the time of
writing, and inventing field names from an untyped spec property would be
guessing at a contract).

## Licence

MIT — see [LICENSE](LICENSE).
