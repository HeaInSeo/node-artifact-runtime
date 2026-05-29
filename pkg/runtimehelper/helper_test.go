package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	if record.LogicalURI != "jumi://runs/run-1/nodes/produce/outputs/report" {
		t.Fatalf("logicalUri = %q, want attemptless logical URI", record.LogicalURI)
	}
	if record.ProducerAttemptID != "attempt-1" {
		t.Fatalf("producerAttemptId = %q, want attempt-1", record.ProducerAttemptID)
	}

	// #nosec G304 -- terminationPath is created under t.TempDir for this test.
	terminationRaw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var terminationManifest provenance.ArtifactManifest
	if err := json.Unmarshal(terminationRaw, &terminationManifest); err != nil {
		t.Fatalf("unmarshal termination manifest: %v", err)
	}
	if terminationManifest.SchemaVersion != provenance.ArtifactManifestSchemaVersion {
		t.Fatalf("termination schemaVersion = %q, want %q", terminationManifest.SchemaVersion, provenance.ArtifactManifestSchemaVersion)
	}
	if terminationManifest.AttemptID != "attempt-1" {
		t.Fatalf("termination attemptId = %q, want attempt-1", terminationManifest.AttemptID)
	}
	if len(terminationManifest.Artifacts) != 1 {
		t.Fatalf("termination artifact count = %d, want 1", len(terminationManifest.Artifacts))
	}
}

func TestRunPromotesOutputToNodeLocalCASAndRecordsLocation(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalRoot := filepath.Join(tmpDir, "var", "lib", "jumi-artifacts")
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-promote",
		SampleRunID:           "sample-promote",
		NodeID:                "worker-2",
		AttemptID:             "attempt-7",
		ContainerName:         "main",
		NodeLocalArtifactRoot: nodeLocalRoot,
		Outputs:               []OutputSpec{{Name: "result", Path: "result.bam", Required: true, Type: "file"}},
		OutputRoot:            tmpDir,
		ManifestPath:          manifestPath,
		TerminationLogPath:    filepath.Join(tmpDir, "termination.log"),
		Command:               []string{"sh", "-c", "printf genome > " + filepath.Join(tmpDir, "result.bam")},
	})
	if exitCode != 0 {
		t.Fatalf("Run() exitCode = %d, want 0", exitCode)
	}

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
	if len(record.Locations) != 1 || record.Locations[0].NodeLocal == nil {
		t.Fatalf("locations = %#v, want one nodeLocal location", record.Locations)
	}
	nodeLocalPath := record.Locations[0].NodeLocal.Path
	if wantPrefix := filepath.Join(nodeLocalRoot, "cas", "sha256"); filepath.Dir(nodeLocalPath) != wantPrefix {
		t.Fatalf("nodeLocalPath dir = %q, want %q", filepath.Dir(nodeLocalPath), wantPrefix)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "result.bam")); err != nil {
		t.Fatalf("original output missing after promotion: %v", err)
	}
	promoted, err := os.ReadFile(nodeLocalPath)
	if err != nil {
		t.Fatalf("read promoted CAS artifact: %v", err)
	}
	if string(promoted) != "genome" {
		t.Fatalf("promoted content = %q, want genome", string(promoted))
	}
	sum := sha256.Sum256(promoted)
	if got, want := record.Digest, "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("record digest = %q, want %q", got, want)
	}
	if got, want := record.SizeBytes, int64(len(promoted)); got != want {
		t.Fatalf("record sizeBytes = %d, want %d", got, want)
	}
}

func TestRunPromotesOutputToExistingCASArtifactAndReusesLocation(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalRoot := filepath.Join(tmpDir, "var", "lib", "jumi-artifacts")
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	payload := []byte("genome")
	sum := sha256.Sum256(payload)
	hexDigest := hex.EncodeToString(sum[:])
	casDir := filepath.Join(nodeLocalRoot, "cas", "sha256")
	if err := os.MkdirAll(casDir, 0o755); err != nil {
		t.Fatalf("mkdir cas dir: %v", err)
	}
	existingPath := filepath.Join(casDir, hexDigest)
	if err := os.WriteFile(existingPath, payload, 0o644); err != nil {
		t.Fatalf("write existing CAS artifact: %v", err)
	}
	infoBefore, err := os.Stat(existingPath)
	if err != nil {
		t.Fatalf("stat existing CAS artifact: %v", err)
	}

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-promote-existing",
		SampleRunID:           "sample-promote-existing",
		NodeID:                "worker-2",
		AttemptID:             "attempt-8",
		ContainerName:         "main",
		NodeLocalArtifactRoot: nodeLocalRoot,
		Outputs:               []OutputSpec{{Name: "result", Path: "result.bam", Required: true, Type: "file"}},
		OutputRoot:            tmpDir,
		ManifestPath:          manifestPath,
		TerminationLogPath:    filepath.Join(tmpDir, "termination.log"),
		Command:               []string{"sh", "-c", "printf genome > " + filepath.Join(tmpDir, "result.bam")},
	})
	if exitCode != 0 {
		t.Fatalf("Run() exitCode = %d, want 0", exitCode)
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest provenance.ArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	record := manifest.Artifacts[0]
	if len(record.Locations) != 1 || record.Locations[0].NodeLocal == nil {
		t.Fatalf("locations = %#v, want one nodeLocal location", record.Locations)
	}
	if got := record.Locations[0].NodeLocal.Path; got != existingPath {
		t.Fatalf("nodeLocalPath = %q, want %q", got, existingPath)
	}
	infoAfter, err := os.Stat(existingPath)
	if err != nil {
		t.Fatalf("stat existing CAS artifact after run: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Fatalf("existing CAS artifact was replaced; modTime before=%s after=%s", infoBefore.ModTime(), infoAfter.ModTime())
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

func TestRunWritesFailureSummaryOnCommandFailure(t *testing.T) {
	tmpDir := t.TempDir()
	terminationPath := filepath.Join(tmpDir, "termination.log")
	exitCode := Run(context.Background(), Config{
		RunID:              "run-6",
		NodeID:             "produce",
		Outputs:            []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
		OutputRoot:         tmpDir,
		ManifestPath:       filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		TerminationLogPath: terminationPath,
		Command:            []string{"sh", "-c", "exit 19"},
	})
	if exitCode != 19 {
		t.Fatalf("Run() exitCode = %d, want 19", exitCode)
	}
	raw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var summary TerminationSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal termination summary: %v", err)
	}
	if summary.Status != "command_failed" {
		t.Fatalf("termination status = %q, want command_failed", summary.Status)
	}
	if summary.ExitCode != 19 {
		t.Fatalf("termination exitCode = %d, want 19", summary.ExitCode)
	}
}

func TestRunMaterializesRemoteFetchInputAndInjectsLocalPath(t *testing.T) {
	tmpDir := t.TempDir()
	payload := []byte("remote-input-ok")
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-7",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		HTTPAllowAny: true,
		OutputRoot:   tmpDir,
		Outputs:      []OutputSpec{{Name: "copied", Path: "copied", Required: true, Type: "file"}},
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command: []string{"sh", "-c", fmt.Sprintf(
			"cat \"$JUMI_INPUT_DATASET_LOCAL_PATH\" > %q",
			filepath.Join(tmpDir, "copied"),
		)},
	})
	if exitCode != 0 {
		t.Fatalf("Run() exitCode = %d, want 0", exitCode)
	}
	localInputPath := filepath.Join(tmpDir, "inputs", "dataset")
	raw, err := os.ReadFile(localInputPath)
	if err != nil {
		t.Fatalf("read materialized input: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("materialized input = %q, want %q", string(raw), string(payload))
	}
}

func TestRunFailsOnRemoteFetchDigestMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong-digest"))
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-8",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		HTTPAllowAny: true,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "inputs", "dataset")); !os.IsNotExist(err) {
		t.Fatalf("materialized input exists after digest mismatch, stat err = %v", err)
	}
}

func TestRunFailsOnRemoteFetchUnsupportedScheme(t *testing.T) {
	tmpDir := t.TempDir()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-8b",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: "file:///tmp/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnRemoteFetchDisallowedHost(t *testing.T) {
	tmpDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("remote-input-ok"))
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:            "run-8c",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		OutputRoot:       tmpDir,
		HTTPAllowedHosts: []string{"artifact-source.local"},
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnRemoteFetchWithoutAllowlistByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	payload := []byte("remote-input-ok")
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-8c-default",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnRemoteFetchRedirectToDisallowedHost(t *testing.T) {
	tmpDir := t.TempDir()
	redirectTarget := "http://disallowed.example/artifact"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	exitCode := Run(context.Background(), Config{
		RunID:            "run-8d",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		OutputRoot:       tmpDir,
		HTTPAllowedHosts: []string{strings.ToLower(parsed.Hostname())},
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnRemoteFetchExpectedSizeMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	payload := []byte("remote-input-ok")
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "99")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-8e",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: expectedDigest, ExpectedSizeBytes: 7, MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		HTTPAllowAny: true,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunMaterializesLocalReuseInputAndInjectsLocalPath(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	payload := []byte("local-reuse-ok")
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-9",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, MaterializationMode: "local_reuse", LocalPath: filepath.Join("inputs", "result")}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		Outputs:               []OutputSpec{{Name: "copied", Path: "copied", Required: true, Type: "file"}},
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command: []string{"sh", "-c", fmt.Sprintf(
			"cat \"$JUMI_INPUT_DATASET_LOCAL_PATH\" > %q",
			filepath.Join(tmpDir, "copied"),
		)},
	})
	if exitCode != 0 {
		t.Fatalf("Run() exitCode = %d, want 0", exitCode)
	}
	localInputPath := filepath.Join(tmpDir, "inputs", "result")
	raw, err := os.ReadFile(localInputPath)
	if err != nil {
		t.Fatalf("read materialized input: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("materialized input = %q, want %q", string(raw), string(payload))
	}
}

func TestRunFailsOnLocalReuseDigestMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, []byte("wrong-digest"), 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-10",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: "sha256:deadbeef", MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "inputs", "dataset")); !os.IsNotExist(err) {
		t.Fatalf("materialized input exists after digest mismatch, stat err = %v", err)
	}
}

func TestRunFailsOnLocalReusePathOutsideAllowedRoot(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	sourcePath := filepath.Join(tmpDir, "outside.bin")
	payload := []byte("outside-root")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-11",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnLocalReuseRelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-12",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: "artifact.bin", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnLocalReuseSymlinkSourcePath(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	targetPath := filepath.Join(nodeLocalDir, "target.bin")
	payload := []byte("symlink-target")
	if err := os.WriteFile(targetPath, payload, 0o644); err != nil {
		t.Fatalf("write target artifact: %v", err)
	}
	sourcePath := filepath.Join(nodeLocalDir, "artifact-link.bin")
	if err := os.Symlink(targetPath, sourcePath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-12b",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnLocalReusePathOutsideInputsSubtree(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o755); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	payload := []byte("outside-inputs")
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-12c",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, MaterializationMode: "local_reuse", LocalPath: "result"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsWhenInputsPathIsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	inputsPath := filepath.Join(tmpDir, "inputs")
	if err := os.Symlink(outsideDir, inputsPath); err != nil {
		t.Fatalf("symlink inputs path: %v", err)
	}
	payload := []byte("remote-input-ok")
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	exitCode := Run(context.Background(), Config{
		RunID:        "run-12d",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: server.URL + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		HTTPAllowAny: true,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestParseInputSpecsFromEnv(t *testing.T) {
	inputs := ParseInputSpecsFromEnv([]string{
		"JUMI_INPUT_DATASET_URI=http://artifact.local/dataset",
		"JUMI_INPUT_DATASET_EXPECTED_DIGEST=sha256:abc",
		"JUMI_INPUT_DATASET_EXPECTED_SIZE_BYTES=17",
		"JUMI_INPUT_DATASET_MATERIALIZATION_MODE=remote_fetch",
		"JUMI_INPUT_DATASET_LOCAL_PATH=inputs/result",
		"JUMI_INPUT_RESULT_NODE_LOCAL_PATH=/jumi-node-artifacts/cas/sha256/def",
		"JUMI_INPUT_RESULT_EXPECTED_DIGEST=sha256:def",
		"JUMI_INPUT_RESULT_MATERIALIZATION_MODE=local_reuse",
		"JUMI_INPUT_REFERENCE_URI=http://artifact.local/reference",
		"JUMI_INPUT_REFERENCE_MATERIALIZATION_MODE=none",
	}, "/work")
	if len(inputs) != 3 {
		t.Fatalf("len(ParseInputSpecsFromEnv()) = %d, want 3", len(inputs))
	}
	if inputs[0].Name != "dataset" || inputs[0].LocalPath != filepath.Join("inputs", "result") {
		t.Fatalf("inputs[0] = %#v", inputs[0])
	}
	if inputs[0].ExpectedDigest != "sha256:abc" {
		t.Fatalf("inputs[0].ExpectedDigest = %q, want sha256:abc", inputs[0].ExpectedDigest)
	}
	if inputs[0].ExpectedSizeBytes != 17 {
		t.Fatalf("inputs[0].ExpectedSizeBytes = %d, want 17", inputs[0].ExpectedSizeBytes)
	}
	if inputs[1].Name != "reference" || inputs[1].MaterializationMode != "none" {
		t.Fatalf("inputs[1] = %#v", inputs[1])
	}
	if inputs[2].Name != "result" || inputs[2].MaterializationMode != "local_reuse" {
		t.Fatalf("inputs[2] = %#v", inputs[2])
	}
	if inputs[2].NodeLocalPath != "/jumi-node-artifacts/cas/sha256/def" {
		t.Fatalf("inputs[2].NodeLocalPath = %q, want node-local path", inputs[2].NodeLocalPath)
	}
}
