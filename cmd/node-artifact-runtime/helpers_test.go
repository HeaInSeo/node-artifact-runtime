package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parseCommaSeparated
// ---------------------------------------------------------------------------

func TestParseCommaSeparated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty_string", "", nil},
		{"whitespace_only", "   ", nil},
		{"single_value", "alpha", []string{"alpha"}},
		{"two_values", "alpha,beta", []string{"alpha", "beta"}},
		{"three_values", "a,b,c", []string{"a", "b", "c"}},
		{"trailing_comma", "a,b,", []string{"a", "b"}},
		{"leading_comma", ",a,b", []string{"a", "b"}},
		{"spaces_around_values", " a , b , c ", []string{"a", "b", "c"}},
		{"all_empty_segments", ",,,", nil},
		{"mixed_empty", "a,,b", []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCommaSeparated(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("parseCommaSeparated(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Fatalf("parseCommaSeparated(%q)[%d] = %q, want %q", tc.input, i, v, tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseBoolEnv
// ---------------------------------------------------------------------------

func TestParseBoolEnv(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"yes", true},
		{"YES", true},
		{"on", true},
		{"ON", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"", false},
		{"   ", false},
		{"random", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parseBoolEnv(tc.input)
			if got != tc.want {
				t.Fatalf("parseBoolEnv(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePositiveInt
// ---------------------------------------------------------------------------

func TestParsePositiveInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"5", 5},
		{"100", 100},
		{"1", 1},
		{"0", 0},
		{"-1", 0},
		{"-100", 0},
		{"", 0},
		{"abc", 0},
		{"  10  ", 10},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parsePositiveInt(tc.input)
			if got != tc.want {
				t.Fatalf("parsePositiveInt(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePositiveInt64
// ---------------------------------------------------------------------------

func TestParsePositiveInt64(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"1000000000", 1000000000},
		{"1", 1},
		{"0", 0},
		{"-5", 0},
		{"", 0},
		{"notanumber", 0},
		{"  42  ", 42},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parsePositiveInt64(tc.input)
			if got != tc.want {
				t.Fatalf("parsePositiveInt64(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePositiveDurationEnv
// ---------------------------------------------------------------------------

func TestParsePositiveDurationEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		want    time.Duration
		wantErr bool
	}{
		{"empty_returns_zero", "", 0, false},
		{"valid_duration_30s", "30s", 30 * time.Second, false},
		{"valid_duration_5m", "5m", 5 * time.Minute, false},
		{"valid_duration_1h", "1h", time.Hour, false},
		{"invalid_string", "notaduration", 0, true},
		{"zero_duration_rejected", "0s", 0, true},
		{"negative_duration_rejected", "-5s", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := "TEST_DURATION_ENV_" + tc.name
			if tc.envVal != "" {
				t.Setenv(key, tc.envVal)
			}
			got, err := parsePositiveDurationEnv(key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePositiveDurationEnv(%q) error = %v, wantErr = %v", tc.envVal, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("parsePositiveDurationEnv(%q) = %v, want %v", tc.envVal, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildConfig — no-contract path
// ---------------------------------------------------------------------------

func TestBuildConfig_NoContract_BasicFields(t *testing.T) {
	cfg, err := buildConfig(
		"", // no contract
		"run-test", "sample-test", "node-test", "att-test", "container-main",
		"output-a,output-b",
		"/work", "/out",
		"/out/_meta/manifest.json",
		"/dev/termination-log",
	)
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if cfg.RunID != "run-test" {
		t.Fatalf("RunID = %q, want run-test", cfg.RunID)
	}
	if cfg.SampleRunID != "sample-test" {
		t.Fatalf("SampleRunID = %q, want sample-test", cfg.SampleRunID)
	}
	if cfg.NodeID != "node-test" {
		t.Fatalf("NodeID = %q, want node-test", cfg.NodeID)
	}
	if cfg.ContainerName != "container-main" {
		t.Fatalf("ContainerName = %q, want container-main", cfg.ContainerName)
	}
	if len(cfg.OutputNames) != 2 {
		t.Fatalf("OutputNames = %v, want 2 elements", cfg.OutputNames)
	}
	if cfg.TerminationLogPath != "/dev/termination-log" {
		t.Fatalf("TerminationLogPath = %q", cfg.TerminationLogPath)
	}
}

func TestBuildConfig_NoContract_EmptyOutputNames(t *testing.T) {
	cfg, err := buildConfig("", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if len(cfg.OutputNames) != 0 {
		t.Fatalf("expected empty OutputNames, got %v", cfg.OutputNames)
	}
}

func TestBuildConfig_NoContract_HTTPAllowedHosts(t *testing.T) {
	t.Setenv("JUMI_ALLOWED_HTTP_SOURCE_HOSTS", "host-a.internal,host-b.internal")

	cfg, err := buildConfig("", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if len(cfg.HTTPAllowedHosts) != 2 {
		t.Fatalf("HTTPAllowedHosts = %v, want 2 elements", cfg.HTTPAllowedHosts)
	}
}

func TestBuildConfig_NoContract_HTTPAllowAny(t *testing.T) {
	t.Setenv("JUMI_ALLOW_ANY_HTTP_SOURCE", "true")

	cfg, err := buildConfig("", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if !cfg.HTTPAllowAny {
		t.Fatal("HTTPAllowAny = false, want true")
	}
}

func TestBuildConfig_NoContract_InvalidHTTPTimeout(t *testing.T) {
	t.Setenv("JUMI_HTTP_SOURCE_TIMEOUT", "notaduration")

	_, err := buildConfig("", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err == nil {
		t.Fatal("expected error for invalid HTTP timeout, got nil")
	}
}

func TestBuildConfig_NoContract_ValidHTTPTimeout(t *testing.T) {
	t.Setenv("JUMI_HTTP_SOURCE_TIMEOUT", "30s")

	cfg, err := buildConfig("", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if cfg.HTTPTimeout != 30*time.Second {
		t.Fatalf("HTTPTimeout = %v, want 30s", cfg.HTTPTimeout)
	}
}

func TestBuildConfig_ContractNotFound_ReturnsError(t *testing.T) {
	_, err := buildConfig("/no/such/contract.json", "r", "s", "n", "a", "c", "", "/w", "/o", "/m", "/t")
	if err == nil {
		t.Fatal("expected error for missing contract file, got nil")
	}
}
