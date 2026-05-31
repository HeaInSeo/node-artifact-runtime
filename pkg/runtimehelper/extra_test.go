package runtimehelper

import (
	"errors"
	"testing"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/contract"
)

// ---------------------------------------------------------------------------
// FirstNonEmpty
// ---------------------------------------------------------------------------

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"all_empty", []string{"", "", ""}, ""},
		{"first_nonempty", []string{"a", "b", "c"}, "a"},
		{"second_nonempty", []string{"", "b", "c"}, "b"},
		{"only_last_nonempty", []string{"", "", "z"}, "z"},
		{"no_args", []string{}, ""},
		{"single_nonempty", []string{"hello"}, "hello"},
		{"single_empty", []string{""}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstNonEmpty(tc.values...)
			if got != tc.want {
				t.Fatalf("FirstNonEmpty(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// classifyHelperError
// ---------------------------------------------------------------------------

func TestClassifyHelperError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "errInvalidConfig",
			err:      errors.Join(errInvalidConfig, errors.New("detail")),
			wantCode: ExitInvalidConfig,
		},
		{
			name:     "errInvalidOutputPath",
			err:      errors.Join(errInvalidOutputPath, errors.New("detail")),
			wantCode: ExitInvalidOutputPath,
		},
		{
			name:     "errMissingRequiredOutput",
			err:      errors.Join(errMissingRequiredOutput, errors.New("detail")),
			wantCode: ExitMissingRequiredOutput,
		},
		{
			name:     "errUnsupportedOutputType",
			err:      errors.Join(errUnsupportedOutputType, errors.New("detail")),
			wantCode: ExitUnsupportedOutputType,
		},
		{
			name:     "errMaterializeFailed",
			err:      errors.Join(errMaterializeFailed, errors.New("detail")),
			wantCode: ExitMaterializeFailed,
		},
		{
			name:     "errManifestWriteFailed",
			err:      errors.Join(errManifestWriteFailed, errors.New("detail")),
			wantCode: ExitManifestWriteFailed,
		},
		{
			name:     "errInspectFailed",
			err:      errors.Join(errInspectFailed, errors.New("detail")),
			wantCode: ExitInspectFailed,
		},
		{
			name:     "unknown_error",
			err:      errors.New("some other failure"),
			wantCode: ExitGenericError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyHelperError(tc.err)
			if got != tc.wantCode {
				t.Fatalf("classifyHelperError() = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ConfigFromContract
// ---------------------------------------------------------------------------

func TestConfigFromContract_Basic(t *testing.T) {
	c := contract.NodeContract{
		RunID:       "run-cc",
		SampleRunID: "sample-cc",
		NodeID:      "node-cc",
		AttemptID:   "att-cc",
		Paths: contract.ContractPaths{
			OutputRoot:   "/out",
			ManifestPath: "/out/_meta/manifest.json",
		},
	}
	cfg := ConfigFromContract(c)
	if cfg.RunID != "run-cc" {
		t.Fatalf("RunID = %q, want run-cc", cfg.RunID)
	}
	if cfg.SampleRunID != "sample-cc" {
		t.Fatalf("SampleRunID = %q, want sample-cc", cfg.SampleRunID)
	}
	if cfg.NodeID != "node-cc" {
		t.Fatalf("NodeID = %q, want node-cc", cfg.NodeID)
	}
	// WorkRoot defaults to /work when empty in contract
	if cfg.WorkRoot != "/work" {
		t.Fatalf("WorkRoot = %q, want /work", cfg.WorkRoot)
	}
	if cfg.OutputRoot != "/out" {
		t.Fatalf("OutputRoot = %q, want /out", cfg.OutputRoot)
	}
	if cfg.ManifestPath != "/out/_meta/manifest.json" {
		t.Fatalf("ManifestPath = %q", cfg.ManifestPath)
	}
}

func TestConfigFromContract_WithInputsOutputs(t *testing.T) {
	c := contract.NodeContract{
		RunID:  "run-io",
		NodeID: "node-io",
		Paths: contract.ContractPaths{
			OutputRoot:   "/out",
			ManifestPath: "/out/_meta/manifest.json",
			WorkRoot:     "/workdir",
		},
		Inputs: []contract.ContractInput{
			{
				Name:                "dataset",
				URI:                 "http://storage.local/dataset",
				ExpectedDigest:      "sha256:abc",
				ExpectedSizeBytes:   1024,
				MaterializationMode: "remote_fetch",
				LocalPath:           "inputs/dataset",
			},
		},
		Outputs: []contract.ContractOutput{
			{Name: "model", Path: "model.bin", Required: true, Type: "file"},
			{Name: "report", Path: "report.txt"},
		},
	}
	cfg := ConfigFromContract(c)
	if cfg.WorkRoot != "/workdir" {
		t.Fatalf("WorkRoot = %q, want /workdir", cfg.WorkRoot)
	}
	if len(cfg.Inputs) != 1 {
		t.Fatalf("len(Inputs) = %d, want 1", len(cfg.Inputs))
	}
	if cfg.Inputs[0].Name != "dataset" {
		t.Fatalf("Inputs[0].Name = %q", cfg.Inputs[0].Name)
	}
	if cfg.Inputs[0].ExpectedSizeBytes != 1024 {
		t.Fatalf("Inputs[0].ExpectedSizeBytes = %d", cfg.Inputs[0].ExpectedSizeBytes)
	}
	if len(cfg.Outputs) != 2 {
		t.Fatalf("len(Outputs) = %d, want 2", len(cfg.Outputs))
	}
	if cfg.Outputs[0].Path != "model.bin" {
		t.Fatalf("Outputs[0].Path = %q", cfg.Outputs[0].Path)
	}
	// Output with no Type should default to "file"
	if cfg.Outputs[1].Type != "file" {
		t.Fatalf("Outputs[1].Type = %q, want file", cfg.Outputs[1].Type)
	}
}

func TestConfigFromContract_FailOnMissingRequiredOutputPromotesRequired(t *testing.T) {
	c := contract.NodeContract{
		RunID:  "run-fmro",
		NodeID: "node-fmro",
		Paths: contract.ContractPaths{
			OutputRoot:   "/out",
			ManifestPath: "/out/_meta/manifest.json",
		},
		Outputs: []contract.ContractOutput{
			{Name: "optional-output", Path: "optional.txt", Required: false},
		},
		Runtime: contract.RuntimePolicy{
			FailOnMissingRequiredOutput: true,
		},
	}
	cfg := ConfigFromContract(c)
	if len(cfg.Outputs) != 1 {
		t.Fatalf("len(Outputs) = %d, want 1", len(cfg.Outputs))
	}
	// FailOnMissingRequiredOutput = true should promote Required to true
	if !cfg.Outputs[0].Required {
		t.Fatal("Outputs[0].Required = false, want true (promoted by FailOnMissingRequiredOutput)")
	}
}

func TestConfigFromContract_RuntimePolicies(t *testing.T) {
	c := contract.NodeContract{
		RunID:  "run-rt",
		NodeID: "node-rt",
		Paths: contract.ContractPaths{
			OutputRoot:   "/out",
			ManifestPath: "/out/_meta/manifest.json",
		},
		Runtime: contract.RuntimePolicy{
			InspectOnSuccessOnly: true,
			AllowDirectoryOutput: true,
		},
	}
	cfg := ConfigFromContract(c)
	if !cfg.InspectOnSuccessOnly {
		t.Fatal("InspectOnSuccessOnly = false, want true")
	}
	if !cfg.AllowDirectoryOutput {
		t.Fatal("AllowDirectoryOutput = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Config.effectiveOutputs
// ---------------------------------------------------------------------------

func TestEffectiveOutputs_WithOutputSpecs(t *testing.T) {
	cfg := Config{
		Outputs: []OutputSpec{
			{Name: "a", Path: "a.bin", Required: true, Type: "file"},
		},
		OutputNames: []string{"b", "c"},
	}
	// When Outputs is non-empty, OutputNames must be ignored
	outs := cfg.effectiveOutputs()
	if len(outs) != 1 {
		t.Fatalf("effectiveOutputs() len = %d, want 1", len(outs))
	}
	if outs[0].Name != "a" {
		t.Fatalf("outs[0].Name = %q, want a", outs[0].Name)
	}
}

func TestEffectiveOutputs_FromOutputNames(t *testing.T) {
	cfg := Config{
		OutputNames: []string{"model", "report", "", "  "},
	}
	outs := cfg.effectiveOutputs()
	// Empty and whitespace-only entries must be skipped
	if len(outs) != 2 {
		t.Fatalf("effectiveOutputs() len = %d, want 2", len(outs))
	}
	if outs[0].Name != "model" {
		t.Fatalf("outs[0].Name = %q, want model", outs[0].Name)
	}
	if outs[1].Name != "report" {
		t.Fatalf("outs[1].Name = %q, want report", outs[1].Name)
	}
	// All generated specs must have Required = true and Type = "file"
	for _, o := range outs {
		if !o.Required {
			t.Errorf("output %q Required = false", o.Name)
		}
		if o.Type != "file" {
			t.Errorf("output %q Type = %q, want file", o.Name, o.Type)
		}
	}
}

func TestEffectiveOutputs_Empty(t *testing.T) {
	cfg := Config{}
	outs := cfg.effectiveOutputs()
	if len(outs) != 0 {
		t.Fatalf("effectiveOutputs() len = %d, want 0", len(outs))
	}
}
