package providerutil

import (
	"context"
	"testing"

	"github.com/xoai/sage-wiki/pkg/provider"
)

type testProvider struct{}

func (*testProvider) Complete(context.Context, provider.CompleteRequest) (*provider.CompleteResponse, error) {
	return nil, nil
}

func (*testProvider) Embed(context.Context, []string) ([][]float32, error) { return nil, nil }

func (*testProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func TestIsNil(t *testing.T) {
	var typedNil *testProvider
	for _, test := range []struct {
		name string
		p    provider.Provider
		want bool
	}{
		{name: "plain nil", p: nil, want: true},
		{name: "typed nil", p: typedNil, want: true},
		{name: "value", p: &testProvider{}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNil(test.p); got != test.want {
				t.Errorf("IsNil() = %v, want %v", got, test.want)
			}
		})
	}
}
