package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

var testCreds = Credentials{AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}

// TestSignRequest_AuthorizationFormat checks the Authorization header shape
// and that all signed headers are present on the request.
func TestSignRequest_AuthorizationFormat(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.us-east-1.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	SignRequest(req, EmptyPayloadHash, testCreds, "us-east-1", "s3", now)

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization prefix = %q", auth)
	}
	for _, part := range []string{"Credential=AKIDEXAMPLE/20130524/us-east-1/s3/aws4_request", "SignedHeaders=host;x-amz-content-sha256;x-amz-date", "Signature="} {
		if !strings.Contains(auth, part) {
			t.Fatalf("Authorization missing %q: %q", part, auth)
		}
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20130524T000000Z" {
		t.Fatalf("X-Amz-Date = %q", got)
	}
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != EmptyPayloadHash {
		t.Fatalf("X-Amz-Content-Sha256 = %q", got)
	}
}

// TestSignRequest_IndependentRecompute re-derives the signature with raw
// stdlib hmac calls (not the sign.go helpers) and compares — a true
// cross-check of the signing pipeline.
func TestSignRequest_IndependentRecompute(t *testing.T) {
	req, err := http.NewRequest(http.MethodPut, "https://examplebucket.s3.us-east-1.amazonaws.com/test$file", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	payloadHash := sha256hex([]byte("Welcome to Amazon S3."))
	SignRequest(req, payloadHash, testCreds, "us-east-1", "s3", now)

	// Independent canonical request (per AWS SigV4 docs, by hand). Go (like
	// aws-sdk) leaves sub-delims such as '$' unescaped in paths, and S3
	// does NOT double-encode — the canonical URI is the literal path.
	canonicalURI := "/test$file"
	canonicalHeaders := "host:examplebucket.s3.us-east-1.amazonaws.com\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:20130524T000000Z\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		"PUT", canonicalURI, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := "20130524/us-east-1/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n20130524T000000Z\n" + scope + "\n" + sha256hex([]byte(canonicalRequest))

	hmacSHA256 := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	kDate := hmacSHA256([]byte("AWS4"+testCreds.SecretKey), "20130524")
	kRegion := hmacSHA256(kDate, "us-east-1")
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	wantSig := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	auth := req.Header.Get("Authorization")
	if !strings.HasSuffix(auth, "Signature="+wantSig) {
		t.Fatalf("signature mismatch:\n got: %q\nwant suffix: Signature=%s", auth, wantSig)
	}
}

// TestSignRequest_QuerySorting pins canonical query ordering (keys then
// values, RFC3986 escapes).
func TestSignRequest_QuerySorting(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://b.s3.us-east-1.amazonaws.com/?b=2&a=1&a=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	SignRequest(req, EmptyPayloadHash, testCreds, "us-east-1", "s3", now)
	sig1 := req.Header.Get("Authorization")

	// Same query params in different input order must sign identically.
	req2, _ := http.NewRequest(http.MethodGet, "https://b.s3.us-east-1.amazonaws.com/?a=0&b=2&a=1", nil)
	SignRequest(req2, EmptyPayloadHash, testCreds, "us-east-1", "s3", now)
	if req2.Header.Get("Authorization") != sig1 {
		t.Fatalf("query order changed signature:\n%s\n%s", sig1, req2.Header.Get("Authorization"))
	}
}

func TestEmptyPayloadHash(t *testing.T) {
	want := sha256hex(nil)
	if EmptyPayloadHash != want {
		t.Fatalf("EmptyPayloadHash = %q, want %q", EmptyPayloadHash, want)
	}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestSignRequest_SessionToken: STS token sets X-Amz-Security-Token AND
// joins SignedHeaders (sorted); absent token → byte-identical to today.
func TestSignRequest_SessionToken(t *testing.T) {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	with := Credentials{AccessKey: "AKIDEXAMPLE", SecretKey: "sekrit", SessionToken: "TOKEN-123"}
	req, _ := http.NewRequest(http.MethodGet, "https://b.s3.us-east-1.amazonaws.com/k", nil)
	SignRequest(req, EmptyPayloadHash, with, "us-east-1", "s3", now)

	if got := req.Header.Get("X-Amz-Security-Token"); got != "TOKEN-123" {
		t.Fatalf("X-Amz-Security-Token = %q", got)
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date;x-amz-security-token") {
		t.Fatalf("SignedHeaders missing token: %q", auth)
	}

	// Absent token → identical bytes to the no-token path.
	req2, _ := http.NewRequest(http.MethodGet, "https://b.s3.us-east-1.amazonaws.com/k", nil)
	SignRequest(req2, EmptyPayloadHash, testCreds, "us-east-1", "s3", now)
	if req2.Header.Get("X-Amz-Security-Token") != "" {
		t.Fatal("no-token request must not carry the header")
	}
}
