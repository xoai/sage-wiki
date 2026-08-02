package config

import (
	"strings"
	"testing"
)

func TestCompileTemperature_DefaultZero(t *testing.T) {
	c := &CompilerConfig{}
	got := c.CompileTemperature()
	if got == nil {
		t.Fatal("CompileTemperature() = nil, want explicit 0 pointer")
	}
	if *got != 0.0 {
		t.Errorf("CompileTemperature() = %v, want 0.0", *got)
	}
}

func TestCompileTemperature_Override(t *testing.T) {
	v := 0.7
	c := &CompilerConfig{Temperature: &v}
	got := c.CompileTemperature()
	if got == nil || *got != 0.7 {
		t.Errorf("CompileTemperature() = %v, want 0.7", got)
	}
}

func TestValidate_TemperatureRange(t *testing.T) {
	base := func() *Config {
		return &Config{
			Project: "t",
			Output:  "wiki",
			Sources: []Source{{Path: "raw"}},
		}
	}
	neg := -0.1
	cfg := base()
	cfg.Compiler.Temperature = &neg
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Errorf("temperature -0.1: want validation error mentioning temperature, got %v", err)
	}
	high := 2.5
	cfg = base()
	cfg.Compiler.Temperature = &high
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Errorf("temperature 2.5: want validation error mentioning temperature, got %v", err)
	}
	ok := 1.0
	cfg = base()
	cfg.Compiler.Temperature = &ok
	if err := cfg.Validate(); err != nil {
		t.Errorf("temperature 1.0: want valid, got %v", err)
	}
}
