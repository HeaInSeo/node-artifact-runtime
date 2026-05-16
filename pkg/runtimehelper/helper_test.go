package runtimehelper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

func TestRunWritesManifestAndTerminationLog(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	terminationPath := filepath.Join(tmpDir, "termination.log")

	exitCode := Run(context.Background(), Config{
		RunID:              "run-1",
		SampleRunID:        "sample-1",
		NodeID:             "produce",
		OutputNames:        []string{"report"},
		OutputRoot:         tmpDir,
		ManifestPath:       manifestPath,
		TerminationLogPath: terminationPath,
		Command:            []string{"sh", "-c", "printf produce-ok > " + filepath.Join(tmpDir, "report")},
	})
	if exitCode != 0 {
		t.Fatalf("Run() exitCode = %d, want 0", exitCode)
	}

	// #nosec G304 -- manifestPath is created under t.TempDir for this test.
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest provenance.ArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(manifest.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(manifest.Artifacts))
	}
	record := manifest.Artifacts[0]
	if record.OutputName != "report" {
		t.Fatalf("outputName = %q, want report", record.OutputName)
	}
	if record.SizeBytes != 10 {
		t.Fatalf("sizeBytes = %d, want 10", record.SizeBytes)
	}
	if record.Digest == "" {
		t.Fatal("digest = empty, want sha256")
	}

	// #nosec G304 -- terminationPath is created under t.TempDir for this test.
	terminationRaw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	if string(terminationRaw) == "" {
		t.Fatal("termination log = empty, want manifest JSON")
	}
}

func TestRunPropagatesChildExitCode(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Run(context.Background(), Config{
		RunID:        "run-2",
		NodeID:       "produce",
		OutputNames:  []string{"report"},
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 17"},
	})
	if exitCode != 17 {
		t.Fatalf("Run() exitCode = %d, want 17", exitCode)
	}
}

func TestParseOutputNames(t *testing.T) {
	got := ParseOutputNames(" report,metrics,, traces ")
	if len(got) != 3 {
		t.Fatalf("len(ParseOutputNames()) = %d, want 3", len(got))
	}
	if got[0] != "report" || got[1] != "metrics" || got[2] != "traces" {
		t.Fatalf("ParseOutputNames() = %#v", got)
	}
}
