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
	cfg, err := buildConfig(contractPath, "run-from-flag", "", "node-from-flag", "attempt-from-flag", "legacy.txt", "/legacy-out", "/legacy-manifest", "/dev/termination-log")
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
	if len(cfg.Outputs) != 1 || cfg.Outputs[0].Path != "report.txt" {
		t.Fatalf("cfg.Outputs = %#v", cfg.Outputs)
	}
}
