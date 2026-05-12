package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestTypeForFile(t *testing.T) {
	projectDir := "/home/user/project"

	tests := []struct {
		name    string
		sources []config.Source
		path    string
		want    string
	}{
		{
			name:    "configured type overrides extension",
			sources: []config.Source{{Path: "raw/adrs", Type: "adr"}},
			path:    "raw/adrs/decision.md",
			want:    "adr",
		},
		{
			name:    "auto falls back to extension detection",
			sources: []config.Source{{Path: "raw", Type: "auto"}},
			path:    "raw/paper.pdf",
			want:    "paper",
		},
		{
			name:    "unset type falls back to extension detection",
			sources: []config.Source{{Path: "raw", Type: ""}},
			path:    "raw/article.md",
			want:    "article",
		},
		{
			name:    "no matching source falls back to extension detection",
			sources: []config.Source{{Path: "raw/adrs", Type: "adr"}},
			path:    "raw/other/code.go",
			want:    "code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Sources: tt.sources}
			got := TypeForFile(projectDir, tt.path, cfg)
			if got != tt.want {
				t.Errorf("TypeForFile(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
