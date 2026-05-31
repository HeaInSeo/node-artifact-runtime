package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildConfigPrefersContract(t *testing.T) {
	tmpDir := t.TempDir()
	contractPath := filepath.Join(tmpDir, "node-contract.json")
	content := `{
  "schemaVersion": "nan.nodeContract.v1",
  "runId": "run-from-contract",
  "nodeId": "node-from-contract",
  "attemptId": "attempt-1",
  "paths": {
    "outputRoot": "/out",
    "manifestPath": "/out/_meta/jumi/manifest.json"
  },
  "inputs": [
    {
      "name": "dataset",
      "uri": "http://artifact.local/dataset",
      "expectedDigest": "sha256:abc",
      "expectedSizeBytes": 17,
      "materializationMode": "remote_fetch",
      "localPath": "inputs/result"
    }
  ],
  "outputs": [
    {
      "name": "report",
      "path": "report.txt",
      "required": true,
      "type": "file"
    }
  ]
}`
	if err := os.WriteFile(contractPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	cfg, err := buildConfig(contractPath, "run-from-flag", "", "node-from-flag", "attempt-from-flag", "", "legacy.txt", "/legacy-work", "/legacy-out", "/legacy-manifest", "/dev/termination-log")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	if cfg.RunID != "run-from-contract" {
		t.Fatalf("cfg.RunID = %q, want run-from-contract", cfg.RunID)
	}
	if cfg.NodeID != "node-from-contract" {
		t.Fatalf("cfg.NodeID = %q, want node-from-contract", cfg.NodeID)
	}
	if cfg.ManifestPath != "/out/_meta/jumi/manifest.json" {
		t.Fatalf("cfg.ManifestPath = %q, want /out/_meta/jumi/manifest.json", cfg.ManifestPath)
	}
	if cfg.WorkRoot != "/work" {
		t.Fatalf("cfg.WorkRoot = %q, want /work", cfg.WorkRoot)
	}
	if len(cfg.Outputs) != 1 || cfg.Outputs[0].Path != "report.txt" {
		t.Fatalf("cfg.Outputs = %#v", cfg.Outputs)
	}
	if len(cfg.Inputs) != 1 || cfg.Inputs[0].Name != "dataset" || cfg.Inputs[0].ExpectedSizeBytes != 17 {
		t.Fatalf("cfg.Inputs = %#v", cfg.Inputs)
	}
}
