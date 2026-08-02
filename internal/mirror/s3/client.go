package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound reports a missing object (404 on GetObject).
var ErrNotFound = errors.New("s3: object not found")

const defaultCallTimeout = 30 * time.Second

// defaultRateBytesPerSec is the assumed minimum uplink for payload scaling:
// the per-attempt timeout is max(30s, 3×(bodyLen/rate)s), capped at 15m
// (item 5 — a large snapshot upload must not die at 30s).
const defaultRateBytesPerSec = 256 * 1024

const maxCallTimeout = 15 * time.Minute

// Client is a minimal S3-compatible client. All calls carry a timeout and
// honor context cancellation (AGENTS.md ground rule 7).
type Client struct {
	endpoint         *url.URL
	region           string
	creds            Credentials
	hc               *http.Client
	pathStyle        bool
	retries          int
	backoff          func(attempt int) time.Duration
	now              func() time.Time
	callTimeoutBase  time.Duration
	callTimeoutCap   time.Duration
	rateBytesPerSec  int64
	fixedCallTimeout time.Duration // WithCallTimeout override (0 = formula)
}

// Option customizes a Client.
type Option func(*Client)

// WithPathStyle selects path-style addressing (bucket in the URL path) —
// required by MinIO; virtual-host style is the default (S3/R2).
func WithPathStyle(on bool) Option {
	return func(c *Client) { c.pathStyle = on }
}

// WithHTTPClient sets the underlying http.Client (e.g. custom transport).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

// WithCallTimeout overrides the per-attempt timeout entirely (0 = formula).
func WithCallTimeout(d time.Duration) Option {
	return func(c *Client) { c.fixedCallTimeout = d }
}

// WithCallTimeoutScale injects the assumed uplink rate for the payload
// scaling formula (tests use it to keep wall-clock small).
func WithCallTimeoutScale(bytesPerSec int64) Option {
	return func(c *Client) {
		if bytesPerSec > 0 {
			c.rateBytesPerSec = bytesPerSec
		}
	}
}

// WithRetries sets the max attempts per call (default 3: initial + 2 retries).
func WithRetries(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.retries = n
		}
	}
}

// WithBackoff overrides the retry backoff (0 = no sleep; for tests).
func WithBackoff(base time.Duration) Option {
	return func(c *Client) {
		c.backoff = func(attempt int) time.Duration { return base << attempt }
	}
}

// NewClient validates the endpoint and returns a Client.
func NewClient(endpoint, region string, creds Credentials, opts ...Option) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("s3: invalid endpoint %q: %w", endpoint, err)
	}
	if region == "" {
		return nil, errors.New("s3: region is required (use \"auto\" for R2/MinIO)")
	}
	c := &Client{
		endpoint:        u,
		region:          region,
		creds:           creds,
		hc:              &http.Client{},
		retries:         3,
		now:             time.Now,
		callTimeoutBase: defaultCallTimeout,
		callTimeoutCap:  maxCallTimeout,
		rateBytesPerSec: defaultRateBytesPerSec,
	}
	c.backoff = func(attempt int) time.Duration { return (100 * time.Millisecond) << attempt }
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// PutObject uploads body to bucket/key.
func (c *Client) PutObject(ctx context.Context, bucket, key string, body []byte) error {
	payloadHash := sha256Hex(body)
	return c.do(ctx, http.MethodPut, bucket, key, nil, body, payloadHash, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return statusError(resp)
		}
		return nil
	})
}

// GetObject downloads bucket/key. ErrNotFound on 404.
func (c *Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	var out []byte
	err := c.do(ctx, http.MethodGet, bucket, key, nil, nil, EmptyPayloadHash, func(resp *http.Response) error {
		if resp.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		if resp.StatusCode != http.StatusOK {
			return statusError(resp)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("s3: read body: %w", err)
		}
		out = b
		return nil
	})
	return out, err
}

// HeadObject reports whether bucket/key exists.
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (bool, error) {
	exists := false
	err := c.do(ctx, http.MethodHead, bucket, key, nil, nil, EmptyPayloadHash, func(resp *http.Response) error {
		switch resp.StatusCode {
		case http.StatusOK:
			exists = true
			return nil
		case http.StatusNotFound:
			return nil
		default:
			return statusError(resp)
		}
	})
	return exists, err
}

// DeleteObject removes bucket/key (204/200 both accepted).
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	return c.do(ctx, http.MethodDelete, bucket, key, nil, nil, EmptyPayloadHash, func(resp *http.Response) error {
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return statusError(resp)
		}
		return nil
	})
}

type listBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// ListObjects returns all keys under prefix (ListObjectsV2, paginated).
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		var page listBucketResult
		err := c.do(ctx, http.MethodGet, bucket, "", q, nil, EmptyPayloadHash, func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return statusError(resp)
			}
			return xml.NewDecoder(resp.Body).Decode(&page)
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, obj.Key)
		}
		if !page.IsTruncated {
			return keys, nil
		}
		token = page.NextContinuationToken
		if token == "" {
			return nil, errors.New("s3: truncated listing without continuation token")
		}
	}
}

// callTimeout computes the per-attempt timeout: a fixed override when set,
// else max(base, 3×(bodyLen/rate)s) capped at cap.
func (c *Client) callTimeout(bodyLen int) time.Duration {
	if c.fixedCallTimeout > 0 {
		return c.fixedCallTimeout
	}
	d := c.callTimeoutBase
	if c.rateBytesPerSec > 0 {
		// Clamp BEFORE multiplying (F-027): an extreme bodyLen/rate ratio
		// could overflow int64 nanoseconds and silently fall to the base.
		secs := int64(bodyLen) / c.rateBytesPerSec
		if secs > int64(c.callTimeoutCap/time.Second)/3 {
			return c.callTimeoutCap
		}
		if scaled := 3 * time.Duration(secs) * time.Second; scaled > d {
			d = scaled
		}
	}
	if d > c.callTimeoutCap {
		d = c.callTimeoutCap
	}
	return d
}

// do executes one request with signing, a PER-ATTEMPT payload-scaled
// timeout passed into that attempt's HTTP request (the request ctx bounds
// dial, TLS, header wait, and body read — a stalled server is truly cut),
// and bounded retry on 5xx/connection errors (never on 4xx). Parent ctx
// still bounds the whole loop; the per-attempt cap is the TCP-level bound.
func (c *Client) do(ctx context.Context, method, bucket, key string, query url.Values, body []byte, payloadHash string, handle func(*http.Response) error) error {
	var lastErr error
	for attempt := 0; attempt < c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoff(attempt - 1)):
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, c.callTimeout(len(body)))
		req, err := c.newRequest(attemptCtx, method, bucket, key, query, body, payloadHash)
		if err != nil {
			cancel()
			return err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("s3: %s %s: %w", method, key, err)
			continue // connection errors (incl. per-attempt timeout) are retryable
		}
		err = handle(resp)
		resp.Body.Close()
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNotFound) || ctx.Err() != nil {
			return err
		}
		var se *httpStatusError
		if errors.As(err, &se) && se.code >= 500 {
			lastErr = err
			continue
		}
		return err // 4xx and handler errors are not retryable
	}
	return fmt.Errorf("s3: %s %s: exhausted %d attempts: %w", method, key, c.retries, lastErr)
}

func (c *Client) newRequest(ctx context.Context, method, bucket, key string, query url.Values, body []byte, payloadHash string) (*http.Request, error) {
	u := *c.endpoint
	if c.pathStyle {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + bucket + keyPathSuffix(key)
	} else {
		u.Host = bucket + "." + u.Host
		u.Path = strings.TrimSuffix(u.Path, "/") + keyPathSuffix(key)
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, fmt.Errorf("s3: build request: %w", err)
	}
	SignRequest(req, payloadHash, c.creds, c.region, "s3", c.now())
	return req, nil
}

// keyPathSuffix returns "/key" or "" — keys always start a new path segment
// under the bucket; the empty key (bucket-level ListObjects) adds nothing.
func keyPathSuffix(key string) string {
	if key == "" {
		return ""
	}
	return "/" + key
}

type httpStatusError struct {
	code int
	body string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("s3: HTTP %d: %s", e.code, e.body) }

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &httpStatusError{code: resp.StatusCode, body: strings.TrimSpace(string(b))}
}
