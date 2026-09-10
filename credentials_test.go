package postforme

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// credentialTokens are the field-name fragments that mark a credential.
//
// THEY ARE ASSEMBLED FROM PIECES, and that is not decoration. This guard scans
// the package it lives in, so a literal "access_token" written here would be
// its own first hit. The alternative is excluding this file, and an exclusion
// blinds a guard to the file it exempts — which is precisely how a guard comes
// to pass while the thing it protects is broken. Nothing is excluded; the
// tokens simply never appear contiguously in this source.
var credentialTokens = []string{
	"access" + "_token",
	"refresh" + "_token",
	"api" + "_key",
	"client" + "_secret",
	"pass" + "word",
	"private" + "_key",
	"bearer" + "_token",
	"credential",
}

// TestNoCredentialFieldsOnDecodedTypes is the security guard the module's
// contract rests on.
//
// THE RULE IT ENFORCES: no struct in this package may carry a JSON-tagged field
// whose name marks it as a credential. The API's SocialAccountDto really does
// return access_token and refresh_token — verified against a live response,
// field names read back from the payload — so the only thing keeping them out
// of this process's memory is that no type here can hold them. encoding/json
// silently discards a key with no matching field, which turns "we did not
// declare it" into "it cannot be decoded".
//
// IT KEYS ON THE JSON TAG, NOT ON EVERY FIELD, and the distinction is load
// bearing: Client.apiKey holds the caller's key by necessity and must stay. It
// has no JSON tag, so it is not part of the wire-decoded surface this guards.
// The rule is about what can arrive FROM the vendor, not what the caller hands
// in.
func TestNoCredentialFieldsOnDecodedTypes(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	scanned, fieldsSeen := 0, 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path) //nolint:gosec // a *.go glob of the package's own directory
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		for _, hit := range jsonTaggedFields(f) {
			fieldsSeen++
			if tok, bad := credentialLike(hit.tag); bad {
				t.Errorf("%s: field %s carries json tag %q, which matches the credential marker %q.\n"+
					"A JSON-tagged credential field makes a vendor token decodable into this process. "+
					"Do not redact it — remove it, so encoding/json drops the key at the boundary.",
					fset.Position(hit.pos), hit.name, hit.tag, tok)
			}
		}
	}

	// ANTI-VACUITY, both halves. A walk that parsed nothing, or that found no
	// tagged fields at all, would make every assertion above trivially true —
	// which is exactly how a guard stops guarding without anyone noticing.
	if scanned == 0 {
		t.Fatal("scanned no non-test source files; this guard is reading the wrong directory")
	}
	if fieldsSeen == 0 {
		t.Fatal("found no JSON-tagged fields at all; the tag extractor is broken, not the package clean")
	}
	t.Logf("scanned %d files, %d JSON-tagged fields, no credential fields", scanned, fieldsSeen)
}

// taggedField is one JSON-tagged struct field.
type taggedField struct {
	name string
	tag  string
	pos  token.Pos
}

// jsonTaggedFields returns every struct field in the file carrying a json tag,
// paired with the tag's name part.
func jsonTaggedFields(f *ast.File) []taggedField {
	var out []taggedField
	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			raw, err := strconv.Unquote(fld.Tag.Value)
			if err != nil {
				continue
			}
			name := strings.Split(reflect.StructTag(raw).Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			switch {
			case len(fld.Names) == 0: // embedded
				out = append(out, taggedField{name: "<embedded>", tag: name, pos: fld.Pos()})
			default:
				for _, id := range fld.Names {
					out = append(out, taggedField{name: id.Name, tag: name, pos: id.Pos()})
				}
			}
		}
		return true
	})
	return out
}

// credentialLike reports whether a json tag name marks a credential, and which
// marker matched. Comparison is on a normalized form so accessToken,
// access-token and AccessToken all match the same marker as access_token.
func credentialLike(tag string) (string, bool) {
	norm := normalizeTag(tag)
	for _, tok := range credentialTokens {
		if strings.Contains(norm, strings.ReplaceAll(tok, "_", "")) {
			return tok, true
		}
	}
	return "", false
}

// normalizeTag lowercases and strips separators, so one marker covers every
// casing convention a vendor might use.
func normalizeTag(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == '_' || r == '-' || r == '.' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestCredentialGuardIsSharp plants the exact fields the vendor really returns
// and proves the walk flags them.
//
// It parses IN-MEMORY source rather than writing a file into the package: a
// guard against credential fields must not be the thing that introduces one,
// even transiently, and a planted file that a failing test run leaves behind
// would do exactly that.
func TestCredentialGuardIsSharp(t *testing.T) {
	planted := []struct {
		name string
		src  string
		want bool
	}{
		{"the real vendor field", `package p
type Account struct {
	ID    string ` + "`json:\"id\"`" + `
	Token string ` + "`json:\"" + "access" + "_token\"`" + `
}`, true},
		{"the other real vendor field", `package p
type Account struct {
	R string ` + "`json:\"" + "refresh" + "_token\"`" + `
}`, true},
		{"camelCase spelling still caught", `package p
type Account struct {
	T string ` + "`json:\"" + "access" + "Token\"`" + `
}`, true},
		{"a credential-free account passes", `package p
type Account struct {
	ID       string ` + "`json:\"id\"`" + `
	Platform string ` + "`json:\"platform\"`" + `
}`, false},
		{"an untagged field named for a key is NOT flagged", `package p
type Client struct {
	apiKey string
}`, false},
	}

	for _, c := range planted {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "planted.go", c.src, 0)
			if err != nil {
				t.Fatalf("parse planted source: %v", err)
			}
			var flagged bool
			for _, hit := range jsonTaggedFields(f) {
				if _, bad := credentialLike(hit.tag); bad {
					flagged = true
				}
			}
			if flagged != c.want {
				t.Fatalf("flagged = %v, want %v", flagged, c.want)
			}
		})
	}
}

// TestAccountCannotHoldATokenAtRuntime is the reflective counterpart to the AST
// walk: it asks the compiled type, not the source.
//
// The two can disagree — a field added through an embedded type from another
// file, or a type alias, would be invisible to a per-file source scan and
// visible here. Neither check subsumes the other, so both run.
func TestAccountCannotHoldATokenAtRuntime(t *testing.T) {
	for _, typ := range []any{Account{}, Post{}, Result{}, Page{}, APIError{}} {
		rt := reflect.TypeOf(typ)
		for i := range rt.NumField() {
			f := rt.Field(i)
			if tok, bad := credentialLike(f.Name); bad {
				t.Errorf("%s.%s matches the credential marker %q", rt.Name(), f.Name, tok)
			}
			if tag := f.Tag.Get("json"); tag != "" {
				if tok, bad := credentialLike(strings.Split(tag, ",")[0]); bad {
					t.Errorf("%s.%s json tag matches the credential marker %q", rt.Name(), f.Name, tok)
				}
			}
		}
	}
}

// TestDecodingDiscardsVendorTokens is the end-to-end proof, and the one that
// matters most: it feeds Account the REAL payload shape — the field names read
// back from a live /social-accounts response — and asserts the tokens are gone
// rather than merely unexported.
func TestDecodingDiscardsVendorTokens(t *testing.T) {
	// The values here are obvious fakes; the KEYS are the real ones.
	body := `{"data":[{
		"id":"spc_test","platform":"linkedin","username":"Example","status":"connected",
		"external_id":null,"user_id":"u_1","profile_photo_url":"https://example.test/p.png",
		"` + "access" + `_token":"NOT-A-REAL-TOKEN-should-be-dropped",
		"` + "refresh" + `_token":"NOT-A-REAL-TOKEN-should-be-dropped",
		"` + "access" + `_token_expires_at":"2026-01-01T00:00:00Z",
		"` + "refresh" + `_token_expires_at":"2026-01-01T00:00:00Z",
		"metadata":{"nested":{"` + "access" + `_token":"also-dropped"}}
	}],"meta":{"total":1,"offset":0,"limit":50,"next":null}}`

	c := newTestClient(t, staticJSON(200, body))
	accts, page, err := c.ListAccounts(t.Context(), AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accts) != 1 || page.Total != 1 {
		t.Fatalf("got %d accounts, page %+v", len(accts), page)
	}

	// Every byte of the decoded value is searched for the planted token text.
	// Not "the field is empty" — the whole struct, formatted, must not contain
	// it anywhere, which also catches a future field that captured it by
	// accident.
	dump := strings.ToLower(formatDeep(accts[0]))
	if strings.Contains(dump, "not-a-real-token") {
		t.Fatalf("a vendor token survived decoding into Account: %s", dump)
	}
	if accts[0].ID != "spc_test" || accts[0].Platform != "linkedin" {
		t.Fatalf("the allowlisted fields did not decode: %+v", accts[0])
	}
	if !accts[0].Connected() {
		t.Error("Connected() should be true for status=connected")
	}
}

// formatDeep renders a value including unexported fields, so the assertion
// above cannot be satisfied by a token merely being unexported.
func formatDeep(v any) string {
	var b strings.Builder
	rv := reflect.ValueOf(v)
	b.WriteString(rv.Type().String() + "{")
	for i := range rv.NumField() {
		f := rv.Field(i)
		b.WriteString(rv.Type().Field(i).Name + ":")
		if f.CanInterface() {
			b.WriteString(strings.TrimSpace(strconv.Quote(toString(f))))
		} else {
			b.WriteString("<unexported>")
		}
		b.WriteString(" ")
	}
	b.WriteString("}")
	return b.String()
}

func toString(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	default:
		return v.String()
	}
}
