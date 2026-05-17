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
		AttemptID:          "attempt-1",
		ContainerName:      "main",
		Outputs:            []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
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
	if manifest.SchemaVersion != provenance.ArtifactManifestSchemaVersion {
		t.Fatalf("schemaVersion = %q, want %q", manifest.SchemaVersion, provenance.ArtifactManifestSchemaVersion)
	}
	if manifest.AttemptID != "attempt-1" {
		t.Fatalf("attemptId = %q, want attempt-1", manifest.AttemptID)
	}
	record := manifest.Artifacts[0]
	if record.OutputName != "report" {
		t.Fatalf("outputName = %q, want report", record.OutputName)
	}
	if record.DeclaredPath != "report" {
		t.Fatalf("declaredPath = %q, want report", record.DeclaredPath)
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
		Outputs:      []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
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

func TestInspectFailsOnMissingRequiredOutput(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Inspect(Config{
		RunID:        "run-3",
		NodeID:       "produce",
		Outputs:      []OutputSpec{{Name: "report", Path: "missing.txt", Required: true, Type: "file"}},
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
	})
	if exitCode != ExitMissingRequiredOutput {
		t.Fatalf("Inspect() exitCode = %d, want %d", exitCode, ExitMissingRequiredOutput)
	}
}

func TestInspectRejectsEscapingOutputPath(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Inspect(Config{
		RunID:        "run-4",
		NodeID:       "produce",
		Outputs:      []OutputSpec{{Name: "report", Path: "../escape.txt", Required: true, Type: "file"}},
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
	})
	if exitCode != ExitInvalidOutputPath {
		t.Fatalf("Inspect() exitCode = %d, want %d", exitCode, ExitInvalidOutputPath)
	}
}

func TestInspectRejectsDirectoryOutput(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "dir-output")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir dir output: %v", err)
	}
	exitCode := Inspect(Config{
		RunID:        "run-5",
		NodeID:       "produce",
		Outputs:      []OutputSpec{{Name: "report", Path: "dir-output", Required: true, Type: "file"}},
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
	})
	if exitCode != ExitUnsupportedOutputType {
		t.Fatalf("Inspect() exitCode = %d, want %d", exitCode, ExitUnsupportedOutputType)
	}
}
