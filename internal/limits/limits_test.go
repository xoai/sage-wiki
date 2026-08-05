package limits

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestResolveZeroValues(t *testing.T) {
	got := Limits{}.Resolve()
	want := Limits{
		MaxDocBytes:                  DefaultMaxDocBytes,
		MaxDocsPerCaptureBatch:       DefaultMaxDocsPerCaptureBatch,
		MaxCompileBatch:              DefaultMaxCompileBatch,
		MaxQueryBytes:                DefaultMaxQueryBytes,
		MaxGraphTraversalNodes:       DefaultMaxGraphTraversalNodes,
		MaxConcurrentProviderCalls:   DefaultMaxConcurrentProviderCalls,
		MaxConcurrentRequestsPerConn: DefaultMaxConcurrentRequestsPerConn,
		ProviderTimeout:              DefaultProviderTimeout,
		CompileDocTimeout:            DefaultCompileDocTimeout,
	}
	if got != want {
		t.Fatalf("Resolve() on zero Limits = %+v, want %+v", got, want)
	}
}

func TestResolvePartialOverride(t *testing.T) {
	got := Limits{MaxDocBytes: 1024, ProviderTimeout: 5 * time.Second}.Resolve()
	if got.MaxDocBytes != 1024 {
		t.Errorf("MaxDocBytes = %d, want 1024", got.MaxDocBytes)
	}
	if got.ProviderTimeout != 5*time.Second {
		t.Errorf("ProviderTimeout = %v, want 5s", got.ProviderTimeout)
	}
	if got.MaxQueryBytes != DefaultMaxQueryBytes {
		t.Errorf("MaxQueryBytes = %d, want default %d", got.MaxQueryBytes, DefaultMaxQueryBytes)
	}
}

func TestLimitErrorMessage(t *testing.T) {
	err := &LimitError{Which: WhichDocBytes, Limit: 100, Got: 200}
	want := "engine: limit exceeded: doc_bytes (limit 100, got 200)"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestLimitErrorUnwrapSentinels(t *testing.T) {
	cases := []struct {
		which    string
		sentinel error
	}{
		{WhichDocBytes, ErrDocTooLarge},
		{WhichCaptureBatch, ErrBatchTooLarge},
		{WhichCompileBatch, ErrBatchTooLarge},
		{WhichQueryBytes, ErrQueryTooLarge},
		{WhichGraphTraversal, ErrTraversalTooWide},
		{WhichEncoding, ErrEncoding},
		{WhichProviderTimeout, ErrTimeout},
		{WhichCompileDocTimeout, ErrTimeout},
	}
	for _, tc := range cases {
		err := &LimitError{Which: tc.which, Limit: 1, Got: 2}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("errors.Is(LimitError{Which:%q}, %v) = false, want true", tc.which, tc.sentinel)
		}
	}
}

func TestLimitErrorUnknownWhichNoPanic(t *testing.T) {
	err := &LimitError{Which: "mystery", Limit: 1, Got: 2}
	if err.Unwrap() != nil {
		t.Errorf("Unwrap() for unknown Which = %v, want nil", err.Unwrap())
	}
	if err.Error() == "" {
		t.Error("Error() for unknown Which must not be empty")
	}
}

func TestLimitErrorThroughWrapping(t *testing.T) {
	base := &LimitError{Which: WhichDocBytes, Limit: 10, Got: 20}
	wrapped := fmt.Errorf("capture failed: %w", base)
	if !errors.Is(wrapped, ErrDocTooLarge) {
		t.Fatal("errors.Is through fmt.Errorf wrapping = false, want true")
	}
	var le *LimitError
	if !errors.As(wrapped, &le) {
		t.Fatal("errors.As through wrapping = false, want true")
	}
	if le.Got != 20 || le.Limit != 10 {
		t.Errorf("recovered LimitError fields = %d/%d, want 10/20", le.Limit, le.Got)
	}
}
