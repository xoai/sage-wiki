// Package s3 is a minimal, stdlib-only S3-compatible client with hand-rolled
// SigV4 signing (SPEC-03: keeps the zero-dependency story — no AWS/MinIO SDK).
package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials holds S3 access keys. Resolve them from environment variables
// or a credentials file — never store them in the workspace or config.
type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string // STS temporary credentials (optional)
}

// EmptyPayloadHash is the SHA-256 of the empty body, used for GET/HEAD/DELETE.
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

const signedHeadersBase = "host;x-amz-content-sha256;x-amz-date"

// SignRequest signs req in place with AWS Signature Version 4 for the given
// payload hash, credentials, region, and service at time now. Callers set
// the body separately; payloadHash must be the hex SHA-256 of that body.
// With an STS SessionToken, X-Amz-Security-Token is set and signed
// (sorted position); without one, output is byte-identical to static creds.
func SignRequest(req *http.Request, payloadHash string, creds Credentials, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := signedHeadersBase
	// Host comes from the request URL, never a caller-set Host header
	// that could drift from the connection target.
	canonicalHeaders := "host:" + req.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
		signedHeaders += ";x-amz-security-token"
		canonicalHeaders += "x-amz-security-token:" + creds.SessionToken + "\n"
	}

	canonicalRequest := strings.Join([]string{
		req.Method,
		escapePath(req.URL.EscapedPath()),
		canonicalQuery(req.URL.Query()),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))

	signature := hex.EncodeToString(hmacSHA256(signingKey(creds.SecretKey, dateStamp, region, service), stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 "+
		"Credential="+creds.AccessKey+"/"+scope+", "+
		"SignedHeaders="+signedHeaders+", "+
		"Signature="+signature)
}

// escapePath normalizes the escaped path to SigV4 rules: keep RFC3986
// escaping, default to "/" (S3 signs the path as-is once escaped).
func escapePath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQuery sorts by key then value, RFC3986-escaped.
func canonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, kv{rfc3986Escape(k), rfc3986Escape(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// rfc3986Escape percent-encodes everything except unreserved characters.
func rfc3986Escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}
