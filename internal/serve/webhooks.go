package serve

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/pkg/events"
)

// SignatureHeader carries the HMAC-SHA256 of the raw delivery body
// (SPEC-07): `sha256=<hex>`. Verifier recipe: docs/webhooks.md.
const SignatureHeader = "X-Sage-Wiki-Signature"

const webhookQueueSize = 256

// WebhookDispatcher delivers events to configured endpoints (SPEC-07):
// per-endpoint type filters, HMAC-SHA256 signatures, bounded retries with
// exponential backoff, and a dead-letter JSONL for permanent failures.
// At-least-once is the OSS contract — no durable exactly-once.
//
// RECORDED DESIGN DECISION (verification review): delivery is sequential —
// one worker, endpoints processed one after another per event. A hung
// endpoint therefore head-of-line-blocks healthy endpoints until the
// queue saturates (worst case ≈ timeout×attempts + backoffs per event).
// Accepted for OSS scope: the engine never stalls (AC-3 holds), the
// queue-drop counter is the observable signal, and per-endpoint workers
// are documented future work.
type WebhookDispatcher struct {
	endpoints  []webhookEndpoint
	deadLetter string
	queue      chan events.Event
	client     *http.Client
	stopOnce   func()
	done       chan struct{}
}

type webhookEndpoint struct {
	url        string
	secret     []byte
	types      map[events.Type]bool // nil = all types
	timeout    time.Duration
	maxRetries int
}

// NewWebhookDispatcher resolves every secret at construction (fail fast —
// an unresolvable secret is a config error, not a runtime surprise) and
// starts the delivery worker derived from ctx.
func NewWebhookDispatcher(ctx context.Context, wsDir string, cfgs []config.WebhookConfig) (*WebhookDispatcher, error) {
	d := &WebhookDispatcher{
		deadLetter: filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"),
		queue:      make(chan events.Event, webhookQueueSize),
		client:     &http.Client{},
		done:       make(chan struct{}),
	}
	for i, cfg := range cfgs {
		secret, err := resolveWebhookSecret(cfg)
		if err != nil {
			return nil, fmt.Errorf("serve.webhooks[%d]: %w", i, err)
		}
		ep := webhookEndpoint{
			url:        cfg.URL,
			secret:     secret,
			timeout:    time.Duration(cfg.TimeoutSecondsOrDefault()) * time.Second,
			maxRetries: cfg.MaxRetriesOrDefault(),
		}
		if len(cfg.Types) > 0 {
			ep.types = make(map[events.Type]bool, len(cfg.Types))
			for _, ty := range cfg.Types {
				ep.types[events.Type(ty)] = true
			}
		}
		d.endpoints = append(d.endpoints, ep)
	}
	wctx, cancel := context.WithCancel(ctx)
	d.stopOnce = cancel
	go d.run(wctx)
	return d, nil
}

// Emit enqueues an event for delivery (non-blocking; a full queue drops
// the event — the bus buffer upstream is the backpressure signal). Queue
// drops join events_dropped_total: a saturated dispatcher (hung endpoint)
// must stay observable (SPEC-07 drop accounting).
func (d *WebhookDispatcher) Emit(ev events.Event) {
	select {
	case d.queue <- ev:
	default:
		metrics.CounterNamed("events_dropped_total").Add(1)
	}
}

// Stop cancels the delivery worker and waits for it to exit.
func (d *WebhookDispatcher) Stop() {
	if d.stopOnce != nil {
		d.stopOnce()
	}
	<-d.done
}

func (d *WebhookDispatcher) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			d.drainResidue()
			return
		case ev := <-d.queue:
			for _, ep := range d.endpoints {
				if ep.matches(ev.Type) {
					d.deliver(ctx, ep, ev)
				}
			}
		}
	}
}

// drainResidue dead-letters whatever is still queued at shutdown — events
// accepted into the queue get a record, never a silent loss (the
// at-least-once contract: undelivered, but observable and replayable from
// the dead letter). No silent failures, Base Principle 2.
func (d *WebhookDispatcher) drainResidue() {
	for {
		select {
		case ev := <-d.queue:
			for _, ep := range d.endpoints {
				if ep.matches(ev.Type) {
					d.deadLetterize(ep, ev, 0, "server stopping: event still queued at shutdown")
				}
			}
		default:
			return
		}
	}
}

func (ep webhookEndpoint) matches(t events.Type) bool {
	return ep.types == nil || ep.types[t]
}

// deliver POSTs the event with retries; permanent failures dead-letter.
func (d *WebhookDispatcher) deliver(ctx context.Context, ep webhookEndpoint, ev events.Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		d.deadLetterize(ep, ev, 0, fmt.Sprintf("marshal: %v", err))
		return
	}
	sig := signBody(ep.secret, body)

	attempts := 0
	backoff := time.Second
	for {
		attempts++
		err := d.post(ctx, ep, body, sig)
		if err == nil {
			return
		}
		if isPermanent(err) || attempts > ep.maxRetries {
			d.deadLetterize(ep, ev, attempts, err.Error())
			return
		}
		// Retry with backoff — but a stopping server wins over sleeping.
		select {
		case <-ctx.Done():
			d.deadLetterize(ep, ev, attempts, "server stopping: "+err.Error())
			return
		case <-time.After(backoff):
		}
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
}

func (d *WebhookDispatcher) post(ctx context.Context, ep webhookEndpoint, body []byte, sig string) error {
	cctx, cancel := context.WithTimeout(ctx, ep.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, ep.url, bytes.NewReader(body))
	if err != nil {
		return permanentError{fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, "sha256="+sig)
	resp, err := d.client.Do(req)
	if err != nil {
		return err // timeout/connection — retryable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return permanentError{fmt.Errorf("endpoint returned %d", resp.StatusCode)}
	}
	return fmt.Errorf("endpoint returned %d", resp.StatusCode)
}

// permanentError marks failures that must not be retried (4xx, bad URL).
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }

func isPermanent(err error) bool {
	_, ok := err.(permanentError)
	return ok
}

// signBody computes the hex HMAC-SHA256 of the raw body.
func signBody(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature recomputes the signature in constant time — shared by
// tests and any in-process consumer (the documented external recipe lives
// in docs/webhooks.md).
func VerifySignature(secret, body []byte, headerValue string) bool {
	want := "sha256=" + signBody(secret, body)
	return hmac.Equal([]byte(want), []byte(headerValue))
}

func (d *WebhookDispatcher) deadLetterize(ep webhookEndpoint, ev events.Event, attempts int, lastErr string) {
	rec := struct {
		Time     string       `json:"time"`
		URL      string       `json:"url"`
		Event    events.Event `json:"event"`
		Attempts int          `json:"attempts"`
		LastErr  string       `json:"last_error"`
	}{
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
		URL:      ep.url,
		Event:    ev,
		Attempts: attempts,
		LastErr:  lastErr,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		log.Warn("webhook dead-letter marshal failed", "url", ep.url, "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(d.deadLetter), 0o755); err != nil {
		log.Warn("webhook dead-letter dir failed", "path", d.deadLetter, "error", err)
		return
	}
	fh, err := os.OpenFile(d.deadLetter, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Warn("webhook dead-letter open failed", "path", d.deadLetter, "error", err)
		return
	}
	defer fh.Close()
	if _, err := fh.Write(append(raw, '\n')); err != nil {
		log.Warn("webhook dead-letter write failed", "path", d.deadLetter, "error", err)
		return
	}
	log.Warn("webhook delivery dead-lettered", "url", ep.url, "attempts", attempts, "error", lastErr)
}

// resolveWebhookSecret reads the HMAC secret from exactly one of env or
// file (config validation already enforced the XOR). The secret never
// appears in logs or events.
func resolveWebhookSecret(cfg config.WebhookConfig) ([]byte, error) {
	switch {
	case cfg.SecretEnv != "":
		v := os.Getenv(cfg.SecretEnv)
		if v == "" {
			return nil, fmt.Errorf("secret_env %q is empty or unset", cfg.SecretEnv)
		}
		return []byte(v), nil
	case cfg.SecretFile != "":
		raw, err := os.ReadFile(cfg.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("secret_file: %w", err)
		}
		v := strings.TrimSpace(string(raw))
		if v == "" {
			return nil, fmt.Errorf("secret_file %q is empty", cfg.SecretFile)
		}
		return []byte(v), nil
	default:
		return nil, fmt.Errorf("no secret source")
	}
}
