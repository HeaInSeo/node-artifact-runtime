package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

func localhostURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = "localhost:" + port
	} else {
		parsed.Host = "localhost"
	}
	return parsed.String()
}

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
	if err := os.MkdirAll(casDir, 0o750); err != nil {
		t.Fatalf("mkdir cas dir: %v", err)
	}
	existingPath := filepath.Join(casDir, hexDigest)
	if err := os.WriteFile(existingPath, payload, 0o600); err != nil {
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
	if err := os.MkdirAll(dirPath, 0o750); err != nil {
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
		RunID:            "run-7",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		HTTPAllowedHosts: []string{"localhost"},
		OutputRoot:       tmpDir,
		Outputs:          []OutputSpec{{Name: "copied", Path: "copied", Required: true, Type: "file"}},
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
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
		RunID:            "run-8",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		HTTPAllowedHosts: []string{"localhost"},
		OutputRoot:       tmpDir,
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
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

func TestRunFailsOnRemoteFetchCredentialBearingUserinfo(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Run(context.Background(), Config{
		RunID:        "run-8b-userinfo",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: "http://user:pass@example.com/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}}, //nolint:gosec // intentional: testing credential-bearing URI rejection
		WorkRoot:     tmpDir,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRunFailsOnRemoteFetchWithAnyQuery(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Run(context.Background(), Config{
		RunID:        "run-8b-query",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: "https://example.com/dataset?download=true", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestValidateRemoteFetchURIRejectsSignedURLQuery(t *testing.T) {
	err := validateRemoteFetchURI(Config{HTTPAllowAny: true}, "https://artifact-source.local/dataset?X-Amz-Signature=abc&X-Amz-Credential=issuer")
	if err == nil {
		t.Fatal("expected signed URL query to be rejected")
	}
	if !strings.Contains(err.Error(), "signed URL query string") {
		t.Fatalf("error = %q, want signed URL query string", err)
	}
}

func TestRunFailsOnRemoteFetchLoopbackHost(t *testing.T) {
	tmpDir := t.TempDir()
	exitCode := Run(context.Background(), Config{
		RunID:        "run-8b-loopback",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: "http://127.0.0.1:8080/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		HTTPAllowAny: true,
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
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
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
		Inputs:       []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
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

	parsed, err := url.Parse(localhostURL(t, server.URL))
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	exitCode := Run(context.Background(), Config{
		RunID:            "run-8d",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
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

func TestRunFailsOnRemoteFetchRedirectTargetWithAnyQuery(t *testing.T) {
	tmpDir := t.TempDir()
	redirectTarget := "https://artifact-source.local/artifact?download=true"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget, http.StatusFound)
	}))
	defer server.Close()

	parsed, err := url.Parse(localhostURL(t, server.URL))
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	exitCode := Run(context.Background(), Config{
		RunID:            "run-8d-query",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		OutputRoot:       tmpDir,
		HTTPAllowedHosts: []string{strings.ToLower(parsed.Hostname()), "artifact-source.local"},
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
		RunID:            "run-8e",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: expectedDigest, ExpectedSizeBytes: 7, MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		HTTPAllowedHosts: []string{"localhost"},
		OutputRoot:       tmpDir,
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestRemoteFetchClientHasDefaultTimeout(t *testing.T) {
	client := remoteFetchClient(Config{})
	if client.Timeout <= 0 {
		t.Fatalf("client.Timeout = %s, want > 0", client.Timeout)
	}
}

func TestRunMaterializesLocalReuseInputAndInjectsLocalPath(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	payload := []byte("local-reuse-ok")
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
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
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("stat source artifact: %v", err)
	}
	localInfo, err := os.Stat(localInputPath)
	if err != nil {
		t.Fatalf("stat materialized input: %v", err)
	}
	if os.SameFile(sourceInfo, localInfo) {
		t.Fatal("local_reuse materialized input must be a copy, not the source file")
	}
	if err := os.WriteFile(localInputPath, []byte("consumer mutation"), 0o600); err != nil {
		t.Fatalf("mutate materialized input: %v", err)
	}
	sourceRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source artifact after materialized input mutation: %v", err)
	}
	if string(sourceRaw) != string(payload) {
		t.Fatalf("source artifact mutated through local_reuse input: got %q want %q", string(sourceRaw), string(payload))
	}
}

func TestRunFailsOnLocalReuseDigestMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, []byte("wrong-digest"), 0o600); err != nil {
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

func TestRunFailsOnLocalReuseExpectedSizeMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	payload := []byte("local-size")
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-10b",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, ExpectedSizeBytes: 1, MaterializationMode: "local_reuse"}},
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

func TestRunFailsOnLocalReusePathOutsideAllowedRoot(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	sourcePath := filepath.Join(tmpDir, "outside.bin")
	payload := []byte("outside-root")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
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
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
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
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	targetPath := filepath.Join(nodeLocalDir, "target.bin")
	payload := []byte("symlink-target")
	if err := os.WriteFile(targetPath, payload, 0o600); err != nil {
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

func TestRunFailsOnLocalReuseResolvedRootEscape(t *testing.T) {
	tmpDir := t.TempDir()
	realRoot := filepath.Join(tmpDir, "real-root")
	if err := os.MkdirAll(realRoot, 0o750); err != nil {
		t.Fatalf("mkdir real root: %v", err)
	}
	symlinkRoot := filepath.Join(tmpDir, "node-local")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	sourcePath := filepath.Join(outsideDir, "artifact.bin")
	payload := []byte("outside-real-root")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}
	sum := sha256.Sum256(payload)
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])

	exitCode := Run(context.Background(), Config{
		RunID:                 "run-12b-realpath",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: sourcePath, ExpectedDigest: expectedDigest, MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: symlinkRoot,
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
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	payload := []byte("outside-inputs")
	sourcePath := filepath.Join(nodeLocalDir, "artifact.bin")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
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
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
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
		RunID:            "run-12d",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		HTTPAllowedHosts: []string{"localhost"},
		OutputRoot:       tmpDir,
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
	})
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
}

func TestParseInputSpecsFromEnv(t *testing.T) {
	inputs, err := ParseInputSpecsFromEnv([]string{
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
	if err != nil {
		t.Fatalf("ParseInputSpecsFromEnv() error = %v", err)
	}
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

func TestParseInputSpecsFromEnvRejectsInvalidExpectedSize(t *testing.T) {
	if _, err := ParseInputSpecsFromEnv([]string{
		"JUMI_INPUT_DATASET_EXPECTED_SIZE_BYTES=abc",
		"JUMI_INPUT_DATASET_MATERIALIZATION_MODE=remote_fetch",
	}, "/work"); err == nil {
		t.Fatal("expected parse error for invalid expected size")
	}
}

func TestConfigValidateRejectsNegativeExpectedSize(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RunID:        "run-negative-size",
		NodeID:       "node-a",
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "artifacts.manifest.json"),
		Inputs:       []InputSpec{{Name: "dataset", ExpectedSizeBytes: -1}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected negative expected size to be rejected")
	}
}

func TestRunTimesOutAndWritesTimeoutSummary(t *testing.T) {
	tmpDir := t.TempDir()
	terminationPath := filepath.Join(tmpDir, "termination.log")
	exitCode := Run(context.Background(), Config{
		RunID:              "run-timeout",
		NodeID:             "produce",
		Outputs:            []OutputSpec{{Name: "report", Path: "report", Required: false, Type: "file"}},
		OutputRoot:         tmpDir,
		ManifestPath:       filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		TerminationLogPath: terminationPath,
		RunTimeout:         50 * time.Millisecond,
		Command:            []string{"sh", "-c", "sleep 60"},
	})
	if exitCode != ExitTimeout {
		t.Fatalf("Run() exitCode = %d, want %d (ExitTimeout)", exitCode, ExitTimeout)
	}
	raw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var summary TerminationSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal termination summary: %v", err)
	}
	if summary.Status != "timeout" {
		t.Fatalf("termination status = %q, want \"timeout\"", summary.Status)
	}
}

func TestRunTimeoutEscalatesToKillWhenChildIgnoresTerm(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	start := time.Now()
	exitCode := Run(context.Background(), Config{
		RunID:               "run-timeout-kill",
		NodeID:              "produce",
		Outputs:             []OutputSpec{{Name: "report", Path: "report", Required: false, Type: "file"}},
		OutputRoot:          tmpDir,
		ManifestPath:        manifestPath,
		TerminationLogPath:  filepath.Join(tmpDir, "termination.log"),
		RunTimeout:          50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
		Command:             []string{"sh", "-c", "trap '' TERM; sleep 60"},
	})
	if exitCode != ExitTimeout {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitTimeout)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run() took %s, want quick SIGKILL escalation", elapsed)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after timeout, stat err = %v", err)
	}
}

func TestRunTimeoutKillsGrandchildProcessGroup(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "grandchild.pid")
	exitCode := Run(context.Background(), Config{
		RunID:               "run-timeout-grandchild",
		NodeID:              "produce",
		OutputRoot:          tmpDir,
		ManifestPath:        filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		RunTimeout:          50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
		Command: []string{"sh", "-c", fmt.Sprintf(
			"sleep 60 & echo $! > %q; wait",
			pidPath,
		)},
	})
	if exitCode != ExitTimeout {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitTimeout)
	}
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", string(raw), err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still exists after timeout", pid)
}

func TestRunFailsWhenBackgroundGrandchildOutlivesMainCommand(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "grandchild.pid")
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	terminationPath := filepath.Join(tmpDir, "termination.log")
	exitCode := Run(context.Background(), Config{
		RunID:               "run-background-grandchild",
		NodeID:              "produce",
		Outputs:             []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
		OutputRoot:          tmpDir,
		ManifestPath:        manifestPath,
		TerminationLogPath:  terminationPath,
		ShutdownGracePeriod: 50 * time.Millisecond,
		Command: []string{"sh", "-c", fmt.Sprintf(
			"printf early > %q; sleep 60 & echo $! > %q; exit 0",
			filepath.Join(tmpDir, "report"),
			pidPath,
		)},
	})
	if exitCode == ExitSuccess {
		t.Fatalf("Run() exitCode = %d, want failure for remaining process group", exitCode)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after process group residue, stat err = %v", err)
	}
	raw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var summary TerminationSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal termination summary: %v", err)
	}
	if summary.Status != "process_group_not_clean" {
		t.Fatalf("termination status = %q, want process_group_not_clean", summary.Status)
	}
	pidRaw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", string(pidRaw), err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still exists after process group residue cleanup", pid)
}

func TestRunExternalSIGTERMSuppressesManifest(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	terminationPath := filepath.Join(tmpDir, "termination.log")
	pidPath := filepath.Join(tmpDir, "grandchild.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperSignalSubprocess") //nolint:gosec // os.Args[0] is this test binary's own path, not attacker input
	cmd.Env = append(os.Environ(),
		"NAN_HELPER_SIGNAL_SUBPROCESS=1",
		"NAN_HELPER_SIGNAL_OUTPUT_ROOT="+tmpDir,
		"NAN_HELPER_SIGNAL_MANIFEST_PATH="+manifestPath,
		"NAN_HELPER_SIGNAL_TERMINATION_PATH="+terminationPath,
		"NAN_HELPER_SIGNAL_PID_PATH="+pidPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("grandchild pid was not written before deadline: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to helper subprocess: %v", err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper subprocess Wait() error = %v, want ExitError", err)
	}
	if exitErr.ExitCode() != signalExitCode(syscall.SIGTERM) {
		t.Fatalf("helper subprocess exit = %d, want %d", exitErr.ExitCode(), signalExitCode(syscall.SIGTERM))
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after external SIGTERM, stat err = %v", err)
	}
	raw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var summary TerminationSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal termination summary: %v", err)
	}
	if summary.Status != "interrupted" && summary.Status != "killed" {
		t.Fatalf("termination status = %q, want interrupted or killed", summary.Status)
	}
}

// TestRunExternalSignalIsForwardedAndObservedByChild extends the SIGTERM-only
// coverage above to SIGINT/SIGHUP/SIGQUIT, and - unlike the SIGTERM test,
// which only checks nan's own exit code and manifest suppression - proves
// the wrapped child actually *received* the specific signal nan forwarded,
// not just that nan itself reacted to it. The child traps each of the four
// signals separately and writes a signal-specific marker file; the test
// asserts only the marker for the signal actually sent exists, and none of
// the others do.
func TestRunExternalSignalIsForwardedAndObservedByChild(t *testing.T) {
	signals := []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT}
	for _, sig := range signals {
		t.Run(sig.String(), func(t *testing.T) {
			tmpDir := t.TempDir()
			manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
			terminationPath := filepath.Join(tmpDir, "termination.log")
			pidPath := filepath.Join(tmpDir, "grandchild.pid")
			markerDir := filepath.Join(tmpDir, "markers")
			if err := os.MkdirAll(markerDir, 0o755); err != nil {
				t.Fatalf("mkdir marker dir: %v", err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperSignalTrapSubprocess") //nolint:gosec // os.Args[0] is this test binary's own path, not attacker input
			cmd.Env = append(os.Environ(),
				"NAN_HELPER_SIGNAL_SUBPROCESS=1",
				"NAN_HELPER_SIGNAL_OUTPUT_ROOT="+tmpDir,
				"NAN_HELPER_SIGNAL_MANIFEST_PATH="+manifestPath,
				"NAN_HELPER_SIGNAL_TERMINATION_PATH="+terminationPath,
				"NAN_HELPER_SIGNAL_PID_PATH="+pidPath,
				"NAN_HELPER_SIGNAL_MARKER_DIR="+markerDir,
			)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start helper subprocess: %v", err)
			}
			defer func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}()

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(pidPath); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if _, err := os.Stat(pidPath); err != nil {
				t.Fatalf("child did not signal readiness before deadline: %v", err)
			}

			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("send %s to helper subprocess: %v", sig, err)
			}
			err := cmd.Wait()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("helper subprocess Wait() error = %v, want ExitError", err)
			}
			if exitErr.ExitCode() != signalExitCode(sig) {
				t.Fatalf("helper subprocess exit = %d, want %d", exitErr.ExitCode(), signalExitCode(sig))
			}

			markers := map[syscall.Signal]string{
				syscall.SIGTERM: "term",
				syscall.SIGINT:  "int",
				syscall.SIGHUP:  "hup",
				syscall.SIGQUIT: "quit",
			}
			wantMarker := markers[sig]
			for candidateSig, name := range markers {
				markerPath := filepath.Join(markerDir, name)
				_, statErr := os.Stat(markerPath)
				gotMarker := statErr == nil
				wantExists := candidateSig == sig
				if gotMarker != wantExists {
					if wantExists {
						t.Errorf("child never trapped %s (expected marker %q, stat err = %v)", sig, wantMarker, statErr)
					} else {
						t.Errorf("child trapped %s but we only sent %s (unexpected marker %q present)", candidateSig, sig, name)
					}
				}
			}
		})
	}
}

// TestRuntimeHelperSignalTrapSubprocess is the child-process entrypoint for
// TestRunExternalSignalIsForwardedAndObservedByChild. The wrapped command
// traps TERM/INT/HUP/QUIT independently so the parent test can tell exactly
// which signal actually reached the child, rather than inferring it from
// nan's own exit code.
func TestRuntimeHelperSignalTrapSubprocess(t *testing.T) {
	if os.Getenv("NAN_HELPER_SIGNAL_SUBPROCESS") != "1" {
		return
	}
	outputRoot := os.Getenv("NAN_HELPER_SIGNAL_OUTPUT_ROOT")
	manifestPath := os.Getenv("NAN_HELPER_SIGNAL_MANIFEST_PATH")
	terminationPath := os.Getenv("NAN_HELPER_SIGNAL_TERMINATION_PATH")
	pidPath := os.Getenv("NAN_HELPER_SIGNAL_PID_PATH")
	markerDir := os.Getenv("NAN_HELPER_SIGNAL_MARKER_DIR")
	script := fmt.Sprintf(
		`trap 'touch %q; exit 0' TERM
trap 'touch %q; exit 0' INT
trap 'touch %q; exit 0' HUP
trap 'touch %q; exit 0' QUIT
echo $$ > %q
while true; do sleep 0.05; done`,
		filepath.Join(markerDir, "term"),
		filepath.Join(markerDir, "int"),
		filepath.Join(markerDir, "hup"),
		filepath.Join(markerDir, "quit"),
		pidPath,
	)
	code := Run(context.Background(), Config{
		RunID:               "run-external-signal-trap",
		NodeID:              "produce",
		Outputs:             []OutputSpec{{Name: "report", Path: "report", Required: false, Type: "file"}},
		OutputRoot:          outputRoot,
		ManifestPath:        manifestPath,
		TerminationLogPath:  terminationPath,
		ShutdownGracePeriod: 200 * time.Millisecond,
		Command:             []string{"sh", "-c", script},
	})
	os.Exit(code)
}

// TestRunDrainsChildStdoutOnFastExit proves the fd-inheritance mechanism
// nan relies on for stdout capture actually holds end-to-end in a real
// subprocess chain (parent test -> nan subprocess -> wrapped child), not
// just by code inspection. The wrapped child bursts a large volume of
// output and exits immediately, with no delay for nan or the OS to "catch
// up" - if anything in the chain buffered-then-dropped instead of relying
// on kernel-level fd inheritance, this would be truncated.
func TestRunDrainsChildStdoutOnFastExit(t *testing.T) {
	const wantLines = 5000
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	terminationPath := filepath.Join(tmpDir, "termination.log")
	capturePath := filepath.Join(tmpDir, "captured-stdout.txt")

	captureFile, err := os.Create(capturePath) //nolint:gosec // test-owned tmp path
	if err != nil {
		t.Fatalf("create capture file: %v", err)
	}
	defer func() { _ = captureFile.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperDrainSubprocess") //nolint:gosec // os.Args[0] is this test binary's own path, not attacker input
	cmd.Env = append(os.Environ(),
		"NAN_HELPER_DRAIN_SUBPROCESS=1",
		"NAN_HELPER_DRAIN_OUTPUT_ROOT="+tmpDir,
		"NAN_HELPER_DRAIN_MANIFEST_PATH="+manifestPath,
		"NAN_HELPER_DRAIN_TERMINATION_PATH="+terminationPath,
		fmt.Sprintf("NAN_HELPER_DRAIN_LINES=%d", wantLines),
	)
	// captureFile is inherited as the subprocess's real os.Stdout fd, exactly
	// like a container runtime wiring PID1's stdout to the container's log
	// pipe - this is the actual production path, not a Go-side io.Writer.
	cmd.Stdout = captureFile
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("helper subprocess failed: %v", err)
	}
	if err := captureFile.Sync(); err != nil {
		t.Fatalf("sync capture file: %v", err)
	}

	raw, err := os.ReadFile(capturePath) //nolint:gosec // test-owned tmp path
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != wantLines {
		t.Fatalf("captured %d lines, want %d (truncated child output)", len(lines), wantLines)
	}
	if want := fmt.Sprintf("line-%d", wantLines); lines[wantLines-1] != want {
		t.Fatalf("last captured line = %q, want %q (child output truncated before completion)", lines[wantLines-1], want)
	}
}

// TestRuntimeHelperDrainSubprocess is the child-process entrypoint for
// TestRunDrainsChildStdoutOnFastExit. It deliberately does not set
// cfg.Stdout, so Run() falls back to the real os.Stdout - the inherited fd
// wired to the parent test's capture file - exercising the same code path
// production traffic takes.
func TestRuntimeHelperDrainSubprocess(t *testing.T) {
	if os.Getenv("NAN_HELPER_DRAIN_SUBPROCESS") != "1" {
		return
	}
	outputRoot := os.Getenv("NAN_HELPER_DRAIN_OUTPUT_ROOT")
	manifestPath := os.Getenv("NAN_HELPER_DRAIN_MANIFEST_PATH")
	terminationPath := os.Getenv("NAN_HELPER_DRAIN_TERMINATION_PATH")
	lines := os.Getenv("NAN_HELPER_DRAIN_LINES")
	script := fmt.Sprintf(`i=1; while [ "$i" -le %s ]; do echo "line-$i"; i=$((i + 1)); done`, lines)
	code := Run(context.Background(), Config{
		RunID:              "run-drain-fast-exit",
		NodeID:             "produce",
		Outputs:            []OutputSpec{{Name: "report", Path: "report", Required: false, Type: "file"}},
		OutputRoot:         outputRoot,
		ManifestPath:       manifestPath,
		TerminationLogPath: terminationPath,
		Command:            []string{"sh", "-c", script},
	})
	os.Exit(code)
}

func TestRunRejectsDirectoryOutputEvenWhenAllowDirectoryOutputSet(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	reportDir := filepath.Join(tmpDir, "report")
	exitCode := Run(context.Background(), Config{
		RunID:                "run-directory-output",
		NodeID:               "produce",
		Outputs:              []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "directory"}},
		OutputRoot:           tmpDir,
		ManifestPath:         manifestPath,
		AllowDirectoryOutput: true,
		Command:              []string{"sh", "-c", "mkdir -p " + reportDir},
	})
	if exitCode != ExitUnsupportedOutputType {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitUnsupportedOutputType)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after unsupported directory output, stat err = %v", err)
	}
}

func TestRuntimeHelperSignalSubprocess(t *testing.T) {
	if os.Getenv("NAN_HELPER_SIGNAL_SUBPROCESS") != "1" {
		return
	}
	outputRoot := os.Getenv("NAN_HELPER_SIGNAL_OUTPUT_ROOT")
	manifestPath := os.Getenv("NAN_HELPER_SIGNAL_MANIFEST_PATH")
	terminationPath := os.Getenv("NAN_HELPER_SIGNAL_TERMINATION_PATH")
	pidPath := os.Getenv("NAN_HELPER_SIGNAL_PID_PATH")
	code := Run(context.Background(), Config{
		RunID:               "run-external-sigterm",
		NodeID:              "produce",
		Outputs:             []OutputSpec{{Name: "report", Path: "report", Required: false, Type: "file"}},
		OutputRoot:          outputRoot,
		ManifestPath:        manifestPath,
		TerminationLogPath:  terminationPath,
		ShutdownGracePeriod: 50 * time.Millisecond,
		Command: []string{"sh", "-c", fmt.Sprintf(
			"sleep 60 & echo $! > %q; wait",
			pidPath,
		)},
	})
	os.Exit(code)
}

func TestAtomicWriteFileContextHonorsCanceledContextBeforeCreate(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "_meta", "artifacts.manifest.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := atomicWriteFileContext(ctx, manifestPath, []byte("{}\n"), 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("atomicWriteFileContext() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after canceled atomic write, stat err = %v", err)
	}
}

func TestReapReparentedChildrenAfterOrphanExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("child subreaper behavior is Linux-specific")
	}
	if err := enableChildSubreaper(); err != nil {
		t.Fatalf("enableChildSubreaper() error = %v", err)
	}
	cmd := exec.Command("sh", "-c", "sleep 0.05 &")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait shell: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	reaped := 0
	for time.Now().Before(deadline) {
		reaped += reapReparentedChildren()
		if reaped > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected to reap at least one reparented child")
}

func TestInspectSkipsOnCommandFailureWhenInspectOnSuccessOnly(t *testing.T) {
	tmpDir := t.TempDir()
	terminationPath := filepath.Join(tmpDir, "termination.log")
	exitCode := Inspect(Config{
		RunID:                "run-inspect-skip",
		NodeID:               "produce",
		Outputs:              []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
		OutputRoot:           tmpDir,
		ManifestPath:         filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		TerminationLogPath:   terminationPath,
		InspectOnSuccessOnly: true,
		CommandExitCode:      42,
	})
	if exitCode != 42 {
		t.Fatalf("Inspect() exitCode = %d, want 42", exitCode)
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
		t.Fatalf("termination status = %q, want \"command_failed\"", summary.Status)
	}
}

func TestInspectProceedsWhenInspectOnSuccessOnlyAndCommandSucceeded(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "report"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	exitCode := Inspect(Config{
		RunID:                "run-inspect-proceed",
		NodeID:               "produce",
		Outputs:              []OutputSpec{{Name: "report", Path: "report", Required: true, Type: "file"}},
		OutputRoot:           tmpDir,
		ManifestPath:         filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		InspectOnSuccessOnly: true,
		CommandExitCode:      0,
	})
	if exitCode != ExitSuccess {
		t.Fatalf("Inspect() exitCode = %d, want %d", exitCode, ExitSuccess)
	}
}
