package contract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadContract(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "node-contract.json")
	data := []byte(`{
  "schemaVersion": "nan.nodeContract.v1",
  "runId": "run-1",
  "sampleRunId": "sample-1",
  "nodeId": "bwa",
  "attemptId": "attempt-1",
  "containerName": "main",
  "paths": {
    "outputRoot": "/out",
    "manifestPath": "/out/_meta/jumi/manifest.json"
  },
  "outputs": [
    {
      "name": "bam",
      "path": "result.bam",
      "required": true,
      "type": "file"
    }
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	contract, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if contract.RunID != "run-1" {
		t.Fatalf("runId = %q, want run-1", contract.RunID)
	}
	if len(contract.Outputs) != 1 {
		t.Fatalf("outputs len = %d, want 1", len(contract.Outputs))
	}
	if contract.Outputs[0].Path != "result.bam" {
		t.Fatalf("output path = %q, want result.bam", contract.Outputs[0].Path)
	}
}

func TestValidateContractRequiresManifestPath(t *testing.T) {
	_, err := Load(writeTempContract(t, `{
  "runId": "run-1",
  "nodeId": "bwa",
  "paths": {
    "outputRoot": "/out"
  }
}`))
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func writeTempContract(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "node-contract.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	return path
}
