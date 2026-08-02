package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Suite cases vendored from botocore's aws4_testsuite (Apache-2.0; LICENSE
// and NOTICE ride along per the license's own §4 — owner flagged in the
// spec). The suite signs generic SigV4 (host;x-amz-date[,x-amz-security-
// token]); our signer is S3-shaped and ALWAYS adds x-amz-content-sha256.
// Expectations are therefore DERIVED: inject x-amz-content-sha256 at the
// sorted position into the vendored canonical request, recompute the
// signature chain with RAW stdlib hmac (independent of sign.go), and
// compare — never an invented expected value.

const (
	suiteCredsAK  = "AKIDEXAMPLE"
	suiteCredsSK  = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	suiteRegion   = "us-east-1"
	suiteService  = "service"
	suiteDateStr  = "20150830T123600Z"
	suiteDateStr2 = "20130524T000000Z"
)

type suiteCase struct {
	name  string
	dir   string
	skip  string // non-empty → skip with this reason
	token string
}

var suiteCases = []suiteCase{
	{name: "get-vanilla-query-order-key-case", dir: "get-vanilla-query-order-key-case"},
	{name: "get-vanilla-with-session-token", dir: "get-vanilla-with-session-token"},
	{name: "get-utf8", dir: "get-utf8"},
	{name: "post-vanilla", dir: "post-vanilla"},
	{name: "post-vanilla-query", dir: "post-vanilla-query"},
	{name: "post-sts-header-after", dir: "post-sts-token/post-sts-header-after"},
}

func readSuiteFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "aws4_testsuite", dir, name))
	if err != nil {
		t.Fatalf("read %s/%s: %v", dir, name, err)
	}
	return string(b)
}

// parseReq parses the vendored .req into method, host, path, query.
func parseReq(t *testing.T, raw string) (method, host, path, query string) {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("empty .req")
	}
	parts := strings.SplitN(lines[0], " ", 3)
	method = parts[0]
	uri := parts[1]
	for _, l := range lines[1:] {
		if strings.HasPrefix(strings.ToLower(l), "host:") {
			host = strings.TrimSpace(l[5:])
			break
		}
	}
	if i := strings.Index(uri, "?"); i >= 0 {
		path, query = uri[:i], uri[i+1:]
	} else {
		path = uri
	}
	return method, host, path, query
}

// TestSigV4Suite_Derived runs the vendored cases against sign.go with
// expectations derived from the vendored canonical request.
func TestSigV4Suite_Derived(t *testing.T) {
	for _, tc := range suiteCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			reqRaw := readSuiteFile(t, tc.dir, tc.dir[strings.LastIndex(tc.dir, "/")+1:]+".req")
			creq := readSuiteFile(t, tc.dir, tc.dir[strings.LastIndex(tc.dir, "/")+1:]+".creq")
			sts := readSuiteFile(t, tc.dir, tc.dir[strings.LastIndex(tc.dir, "/")+1:]+".sts")

			method, host, path, query := parseReq(t, reqRaw)
			// Session token: extracted from the vendored .creq when present;
			// post-sts-header-after carries NONE by design (botocore adds it
			// after signing) — inject the botocore harness token so the case
			// actually exercises the token path (F-028).
			token := ""
			for _, l := range strings.Split(creq, "\n") {
				if strings.HasPrefix(l, "x-amz-security-token:") {
					token = strings.TrimPrefix(l, "x-amz-security-token:")
					break
				}
			}
			if token == "" && strings.Contains(tc.dir, "post-sts") {
				token = "6e86291e8372ff2a2260956d9b8aae1d763fbf315fa00fa31553b73ebf194267" // botocore harness token
			}
			date := suiteDateStr2
			if strings.Contains(creq, "20150830T123600Z") || strings.Contains(sts, "20150830T123600Z") {
				date = suiteDateStr
			}
			now, err := time.Parse("20060102T150405Z", date)
			if err != nil {
				t.Fatalf("parse date %q: %v", date, err)
			}

			// Build the request and sign with our code.
			rawURL := "https://" + host + path
			if query != "" {
				rawURL += "?" + query
			}
			req, err := http.NewRequest(method, rawURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			payloadHash := EmptyPayloadHash // all six vendored cases are bodiless
			SignRequest(req, payloadHash, Credentials{AccessKey: suiteCredsAK, SecretKey: suiteCredsSK, SessionToken: token}, suiteRegion, suiteService, now)

			// DERIVE the expected signature: take the vendored canonical
			// request, inject x-amz-content-sha256 at the sorted header
			// position, recompute with raw stdlib.
			wantSig := deriveSignature(t, creq, sts, payloadHash, date, token)

			// Harness self-check (review issue 6): deriving WITHOUT
			// injection must reproduce AWS's published .authz signature
			// exactly — anchors the derivation end-to-end to ground truth.
			authz := readSuiteFile(t, tc.dir, tc.dir[strings.LastIndex(tc.dir, "/")+1:]+".authz")
			vendorSig := deriveVendorSignature(t, creq, sts)
			if !strings.Contains(authz, "Signature="+vendorSig) {
				t.Fatalf("derivation disagrees with vendored .authz: %q vs computed %q", authz, vendorSig)
			}

			auth := req.Header.Get("Authorization")
			if !strings.HasSuffix(auth, "Signature="+wantSig) {
				t.Fatalf("signature mismatch:\n got: %q\nwant suffix: Signature=%s", auth, wantSig)
			}
		})
	}
}

// deriveSignature recomputes the expected signature from the vendored
// canonical request with x-amz-content-sha256 injected at the sorted
// position (labeled derived, per spec — the vendored files sign without it).
func deriveSignature(t *testing.T, creq, sts, payloadHash, amzDate, token string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(creq, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("malformed .creq (%d lines)", len(lines))
	}
	// .creq layout: method / uri / query(may be empty) / headers(+blank) /
	// signed-headers / payload. The separator is the first blank line AT OR
	// AFTER index 3 (the query line at index 2 may itself be empty).
	blank := -1
	for i := 3; i < len(lines); i++ {
		if lines[i] == "" {
			blank = i
			break
		}
	}
	if blank < 0 {
		t.Fatal("no header/signed-headers separator in .creq")
	}
	headers := append([]string{}, lines[3:blank]...)
	signedHdrs := lines[blank+1]
	payload := ""
	if blank+2 < len(lines) {
		payload = lines[blank+2]
	}

	// Inject x-amz-content-sha256 at the sorted position among headers.
	injected := "x-amz-content-sha256:" + payloadHash
	headers = append(headers, injected)
	sortHeaderLines(headers)
	// Signed-headers list is also sorted — rebuild from the sorted headers.
	var sh []string
	for _, h := range headers {
		sh = append(sh, strings.ToLower(strings.SplitN(h, ":", 2)[0]))
	}
	signedHdrs = strings.Join(sh, ";")
	if token != "" && !headerPresent(headers, "x-amz-security-token") {
		// The vendored .creq may already carry the token header (it does for
		// the STS cases) — appending again would duplicate it (the F-bug:
		// two x-amz-security-token lines in one canonical request).
		tokHdr := "x-amz-security-token:" + token
		headers = append(headers, tokHdr)
		sortHeaderLines(headers)
		sh = sh[:0]
		for _, h := range headers {
			sh = append(sh, strings.ToLower(strings.SplitN(h, ":", 2)[0]))
		}
		signedHdrs = strings.Join(sh, ";")
	}

	derivedCreq := strings.Join([]string{
		lines[0], lines[1], lines[2],
		strings.Join(headers, "\n") + "\n",
		signedHdrs,
		payload,
	}, "\n")

	scope := strings.Split(sts, "\n")[2]
	hmacSHA256 := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	kDate := hmacSHA256([]byte("AWS4"+suiteCredsSK), amzDate[:8])
	kRegion := hmacSHA256(kDate, suiteRegion)
	kService := hmacSHA256(kRegion, suiteService)
	kSigning := hmacSHA256(kService, "aws4_request")
	derivedSts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256HexStr(derivedCreq)
	return hex.EncodeToString(hmacSHA256(kSigning, derivedSts))
}

func headerPresent(headers []string, name string) bool {
	for _, h := range headers {
		if strings.ToLower(strings.SplitN(h, ":", 2)[0]) == name {
			return true
		}
	}
	return false
}

func sortHeaderLines(headers []string) {
	for i := 0; i < len(headers)-1; i++ {
		for j := i + 1; j < len(headers); j++ {
			ki := strings.ToLower(strings.SplitN(headers[i], ":", 2)[0])
			kj := strings.ToLower(strings.SplitN(headers[j], ":", 2)[0])
			if ki > kj {
				headers[i], headers[j] = headers[j], headers[i]
			}
		}
	}
}

func sha256HexStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// deriveVendorSignature recomputes the vendored canonical request VERBATIM
// (no injection) — must equal AWS's published signature in .authz.
func deriveVendorSignature(t *testing.T, creq, sts string) string {
	t.Helper()
	scope := strings.Split(sts, "\n")[2]
	amzDate := strings.Split(sts, "\n")[1]
	hm := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	kDate := hm([]byte("AWS4"+suiteCredsSK), amzDate[:8])
	kRegion := hm(kDate, suiteRegion)
	kService := hm(kRegion, suiteService)
	kSigning := hm(kService, "aws4_request")
	derivedSts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256HexStr(strings.TrimRight(creq, "\n"))
	return hex.EncodeToString(hm(kSigning, derivedSts))
}
