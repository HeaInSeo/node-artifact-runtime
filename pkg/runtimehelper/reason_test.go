package runtimehelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestTerminationSummaryReasonIsAdditive verifies the structured Reason field
// is additive: a summary without a reason serializes identically to before
// (no "reason" key, status/message intact), and a summary with a reason adds
// the key without disturbing existing fields. This is the backward-compat
// guarantee for existing status/message consumers.
func TestTerminationSummaryReasonIsAdditive(t *testing.T) {
	withoutReason := TerminationSummary{Status: "materialization_failed", ExitCode: ExitMaterializeFailed, Message: "boom"}
	raw, err := json.Marshal(withoutReason)
	if err != nil {
		t.Fatalf("marshal summary without reason: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal summary without reason: %v", err)
	}
	if _, present := decoded["reason"]; present {
		t.Fatalf("reason key must be omitted when unset, got %s", raw)
	}
	if decoded["status"] != "materialization_failed" || decoded["message"] != "boom" {
		t.Fatalf("existing fields altered: %s", raw)
	}

	withReasonSummary := withoutReason
	withReasonSummary.Reason = string(ReasonDigestMismatch)
	raw, err = json.Marshal(withReasonSummary)
	if err != nil {
		t.Fatalf("marshal summary with reason: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal summary with reason: %v", err)
	}
	if decoded["reason"] != "digest_mismatch" {
		t.Fatalf("reason = %v, want digest_mismatch (%s)", decoded["reason"], raw)
	}
	if decoded["status"] != "materialization_failed" || decoded["message"] != "boom" {
		t.Fatalf("existing fields altered when reason set: %s", raw)
	}
}

// TestMaterializeReasonPreservesErrorChain verifies that tagging a
// materialization error with a structured reason does not break the existing
// errMaterializeFailed classification or the free-form message, and that
// unclassified failures carry no reason.
func TestMaterializeReasonPreservesErrorChain(t *testing.T) {
	base := fmt.Errorf("%w: input dataset digest mismatch", errMaterializeFailed)
	tagged := withReason(ReasonDigestMismatch, base)
	if !errors.Is(tagged, errMaterializeFailed) {
		t.Fatal("tagged error lost errMaterializeFailed in chain")
	}
	if tagged.Error() != base.Error() {
		t.Fatalf("tagged error message = %q, want %q", tagged.Error(), base.Error())
	}
	if got := materializeReasonOf(tagged); got != ReasonDigestMismatch {
		t.Fatalf("materializeReasonOf(tagged) = %q, want %q", got, ReasonDigestMismatch)
	}
	if got := materializeReasonOf(base); got != "" {
		t.Fatalf("materializeReasonOf(unclassified) = %q, want empty", got)
	}
}

// TestRunRemoteFetchDigestMismatchSetsReason exercises the full Run path and
// asserts both the preserved status/message and the additive reason.
func TestRunRemoteFetchDigestMismatchSetsReason(t *testing.T) {
	summary := runRemoteFetchFailure(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong-digest"))
	}, "sha256:deadbeef")
	if summary.Status != "materialization_failed" {
		t.Fatalf("status = %q, want materialization_failed", summary.Status)
	}
	if summary.Message == "" {
		t.Fatal("message must be preserved alongside reason")
	}
	if summary.Reason != string(ReasonDigestMismatch) {
		t.Fatalf("reason = %q, want %q", summary.Reason, ReasonDigestMismatch)
	}
}

// TestRunRemoteFetchNonOKSetsRemoteUnavailableReason maps a non-200 response to
// remote_unavailable.
func TestRunRemoteFetchNonOKSetsRemoteUnavailableReason(t *testing.T) {
	summary := runRemoteFetchFailure(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}, "sha256:deadbeef")
	if summary.Reason != string(ReasonRemoteUnavailable) {
		t.Fatalf("reason = %q, want %q", summary.Reason, ReasonRemoteUnavailable)
	}
}

// TestRunRemoteFetchRejectedURISetsPathRejectedReason maps a policy-rejected
// URI (unsupported scheme) to path_rejected.
func TestRunRemoteFetchRejectedURISetsPathRejectedReason(t *testing.T) {
	tmpDir := t.TempDir()
	summary := runToSummary(t, tmpDir, Config{
		RunID:        "reason-path-rejected",
		NodeID:       "consume",
		Inputs:       []InputSpec{{Name: "dataset", URI: "file:///tmp/dataset", ExpectedDigest: "sha256:deadbeef", MaterializationMode: "remote_fetch"}},
		WorkRoot:     tmpDir,
		OutputRoot:   tmpDir,
		ManifestPath: filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:      []string{"sh", "-c", "exit 0"},
	})
	if summary.Reason != string(ReasonPathRejected) {
		t.Fatalf("reason = %q, want %q", summary.Reason, ReasonPathRejected)
	}
}

// TestRunLocalReuseMissingSourceSetsReason maps an absent node-local source to
// local_source_missing (rather than path_rejected).
func TestRunLocalReuseMissingSourceSetsReason(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	missingSource := filepath.Join(nodeLocalDir, "does-not-exist.bin")
	summary := runToSummary(t, tmpDir, Config{
		RunID:                 "reason-local-missing",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: missingSource, ExpectedDigest: "sha256:deadbeef", MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if summary.Reason != string(ReasonLocalSourceMissing) {
		t.Fatalf("reason = %q, want %q", summary.Reason, ReasonLocalSourceMissing)
	}
}

// TestRunLocalReuseOutsideRootSetsPathRejectedReason keeps a genuine policy
// rejection (source outside the allowed root) mapped to path_rejected.
func TestRunLocalReuseOutsideRootSetsPathRejectedReason(t *testing.T) {
	tmpDir := t.TempDir()
	nodeLocalDir := filepath.Join(tmpDir, "node-local")
	if err := os.MkdirAll(nodeLocalDir, 0o750); err != nil {
		t.Fatalf("mkdir node-local dir: %v", err)
	}
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	outsideSource := filepath.Join(outsideDir, "artifact.bin")
	if err := os.WriteFile(outsideSource, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	summary := runToSummary(t, tmpDir, Config{
		RunID:                 "reason-outside-root",
		NodeID:                "consume",
		Inputs:                []InputSpec{{Name: "dataset", NodeLocalPath: outsideSource, ExpectedDigest: "sha256:deadbeef", MaterializationMode: "local_reuse"}},
		WorkRoot:              tmpDir,
		NodeLocalArtifactRoot: nodeLocalDir,
		OutputRoot:            tmpDir,
		ManifestPath:          filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:               []string{"sh", "-c", "exit 0"},
	})
	if summary.Reason != string(ReasonPathRejected) {
		t.Fatalf("reason = %q, want %q", summary.Reason, ReasonPathRejected)
	}
}

// runRemoteFetchFailure runs a remote_fetch materialization against a test
// server whose handler is expected to produce a materialization failure, and
// returns the parsed termination summary.
func runRemoteFetchFailure(t *testing.T, handler http.HandlerFunc, expectedDigest string) TerminationSummary {
	t.Helper()
	tmpDir := t.TempDir()
	server := httptest.NewServer(handler)
	defer server.Close()
	return runToSummary(t, tmpDir, Config{
		RunID:            "reason-remote",
		NodeID:           "consume",
		Inputs:           []InputSpec{{Name: "dataset", URI: localhostURL(t, server.URL) + "/dataset", ExpectedDigest: expectedDigest, MaterializationMode: "remote_fetch"}},
		WorkRoot:         tmpDir,
		HTTPAllowedHosts: []string{"localhost"},
		OutputRoot:       tmpDir,
		ManifestPath:     filepath.Join(tmpDir, "_meta", "artifacts.manifest.json"),
		Command:          []string{"sh", "-c", "exit 0"},
	})
}

// runToSummary runs cfg (writing its termination log under tmpDir), expects a
// materialization failure exit code, and returns the parsed summary.
func runToSummary(t *testing.T, tmpDir string, cfg Config) TerminationSummary {
	t.Helper()
	terminationPath := filepath.Join(tmpDir, "termination.log")
	cfg.TerminationLogPath = terminationPath
	exitCode := Run(context.Background(), cfg)
	if exitCode != ExitMaterializeFailed {
		t.Fatalf("Run() exitCode = %d, want %d", exitCode, ExitMaterializeFailed)
	}
	raw, err := os.ReadFile(terminationPath)
	if err != nil {
		t.Fatalf("read termination log: %v", err)
	}
	var summary TerminationSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("unmarshal termination summary: %v", err)
	}
	return summary
}
