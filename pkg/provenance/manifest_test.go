package provenance

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ParseArtifactManifest — happy paths
// ---------------------------------------------------------------------------

func TestParseArtifactManifest_EmptyArtifacts(t *testing.T) {
	input := `{"schemaVersion":"nan.artifactManifest.v1","runId":"run-1","artifacts":[]}`
	m, err := ParseArtifactManifest([]byte(input))
	if err != nil {
		t.Fatalf("ParseArtifactManifest: %v", err)
	}
	if m.RunID != "run-1" {
		t.Fatalf("RunID = %q, want run-1", m.RunID)
	}
	if len(m.Artifacts) != 0 {
		t.Fatalf("len(Artifacts) = %d, want 0", len(m.Artifacts))
	}
}

func TestParseArtifactManifest_SingleArtifact(t *testing.T) {
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		RunID:         "run-42",
		SampleRunID:   "sample-42",
		NodeID:        "node-compute",
		AttemptID:     "att-1",
		OutputRoot:    "/out",
		Artifacts: []ArtifactRecord{
			{
				OutputName:   "model",
				DeclaredPath: "model.pt",
				AbsolutePath: "/out/model.pt",
				Digest:       "sha256:deadbeef",
				SizeBytes:    4096,
				Type:         "file",
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseArtifactManifest(data)
	if err != nil {
		t.Fatalf("ParseArtifactManifest: %v", err)
	}
	if got.RunID != "run-42" {
		t.Fatalf("RunID = %q, want run-42", got.RunID)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(got.Artifacts))
	}
	if got.Artifacts[0].Digest != "sha256:deadbeef" {
		t.Fatalf("Digest = %q, want sha256:deadbeef", got.Artifacts[0].Digest)
	}
}

func TestParseArtifactManifest_MultipleArtifacts(t *testing.T) {
	input := `{
		"schemaVersion":"nan.artifactManifest.v1",
		"artifacts":[
			{"outputName":"a","digest":"sha256:aaa"},
			{"outputName":"b","digest":"sha256:bbb"},
			{"outputName":"c","sizeBytes":100}
		]
	}`
	m, err := ParseArtifactManifest([]byte(input))
	if err != nil {
		t.Fatalf("ParseArtifactManifest: %v", err)
	}
	if len(m.Artifacts) != 3 {
		t.Fatalf("len(Artifacts) = %d, want 3", len(m.Artifacts))
	}
}

func TestParseArtifactManifest_WithLocationsAndProvenance(t *testing.T) {
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		Artifacts: []ArtifactRecord{
			{
				OutputName: "dataset",
				Locations: []ArtifactLocation{
					{NodeLocal: &NodeLocalLocation{NodeName: "worker-1", Path: "/cache/dataset"}},
				},
				Provenance: &ArtifactLineage{
					Inputs: []ArtifactLineageInput{
						{
							InputName:          "raw",
							ArtifactDigest:     "sha256:raw",
							ProducerLogicalURI: "jumi://runs/run-0/nodes/ingest/outputs/raw",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(manifest)
	got, err := ParseArtifactManifest(data)
	if err != nil {
		t.Fatalf("ParseArtifactManifest: %v", err)
	}
	if got.Artifacts[0].Provenance == nil {
		t.Fatal("Provenance is nil")
	}
	if len(got.Artifacts[0].Provenance.Inputs) != 1 {
		t.Fatalf("len(Inputs) = %d, want 1", len(got.Artifacts[0].Provenance.Inputs))
	}
	if got.Artifacts[0].Locations[0].NodeLocal.NodeName != "worker-1" {
		t.Fatalf("NodeName = %q, want worker-1", got.Artifacts[0].Locations[0].NodeLocal.NodeName)
	}
}

// ---------------------------------------------------------------------------
// ParseArtifactManifest — error paths
// ---------------------------------------------------------------------------

func TestParseArtifactManifest_InvalidJSON(t *testing.T) {
	_, err := ParseArtifactManifest([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseArtifactManifest_MissingOutputName(t *testing.T) {
	input := `{"artifacts":[{"digest":"sha256:abc"}]}`
	_, err := ParseArtifactManifest([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing outputName, got nil")
	}
	if !strings.Contains(err.Error(), "outputName") {
		t.Fatalf("error %q does not mention outputName", err.Error())
	}
}

func TestParseArtifactManifest_DuplicateOutputName(t *testing.T) {
	input := `{"artifacts":[{"outputName":"dupe"},{"outputName":"dupe"}]}`
	_, err := ParseArtifactManifest([]byte(input))
	if err == nil {
		t.Fatal("expected error for duplicate outputName, got nil")
	}
	if !strings.Contains(err.Error(), "dupe") {
		t.Fatalf("error %q does not mention duplicate name", err.Error())
	}
}

func TestParseArtifactManifest_EmptyBytes(t *testing.T) {
	_, err := ParseArtifactManifest([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// ---------------------------------------------------------------------------
// ArtifactManifest.ByOutputName
// ---------------------------------------------------------------------------

func TestArtifactManifest_ByOutputName(t *testing.T) {
	m := ArtifactManifest{
		Artifacts: []ArtifactRecord{
			{OutputName: "alpha", Digest: "sha256:a"},
			{OutputName: "beta", Digest: "sha256:b"},
		},
	}

	t.Run("found", func(t *testing.T) {
		rec, ok := m.ByOutputName("alpha")
		if !ok {
			t.Fatal("ByOutputName(alpha): not found")
		}
		if rec.Digest != "sha256:a" {
			t.Fatalf("Digest = %q, want sha256:a", rec.Digest)
		}
	})

	t.Run("found_second", func(t *testing.T) {
		rec, ok := m.ByOutputName("beta")
		if !ok {
			t.Fatal("ByOutputName(beta): not found")
		}
		if rec.Digest != "sha256:b" {
			t.Fatalf("Digest = %q, want sha256:b", rec.Digest)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		_, ok := m.ByOutputName("gamma")
		if ok {
			t.Fatal("expected not found for gamma")
		}
	})

	t.Run("empty_manifest", func(t *testing.T) {
		empty := ArtifactManifest{}
		_, ok := empty.ByOutputName("anything")
		if ok {
			t.Fatal("expected not found on empty manifest")
		}
	})
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	if DefaultArtifactsManifestPath == "" {
		t.Fatal("DefaultArtifactsManifestPath is empty")
	}
	if ArtifactManifestSchemaVersion == "" {
		t.Fatal("ArtifactManifestSchemaVersion is empty")
	}
}

// ---------------------------------------------------------------------------
// ArtifactRecord field coverage
// ---------------------------------------------------------------------------

func TestParseArtifactManifest_AllRecordFields(t *testing.T) {
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		ContainerName: "main",
		NanVersion:    "v1.2.3",
		CreatedAt:     "2024-01-01T00:00:00Z",
		Artifacts: []ArtifactRecord{
			{
				OutputName:        "report",
				DeclaredPath:      "report.pdf",
				AbsolutePath:      "/out/report.pdf",
				URI:               "http://storage.local/report.pdf",
				LogicalURI:        "jumi://runs/run-1/nodes/n/outputs/report",
				Digest:            "sha256:feed",
				SizeBytes:         8192,
				Type:              "file",
				ProducerAttemptID: "att-2",
			},
		},
	}
	data, _ := json.Marshal(manifest)
	got, err := ParseArtifactManifest(data)
	if err != nil {
		t.Fatalf("ParseArtifactManifest: %v", err)
	}
	r := got.Artifacts[0]
	if r.URI != "http://storage.local/report.pdf" {
		t.Fatalf("URI = %q", r.URI)
	}
	if r.LogicalURI != "jumi://runs/run-1/nodes/n/outputs/report" {
		t.Fatalf("LogicalURI = %q", r.LogicalURI)
	}
	if r.ProducerAttemptID != "att-2" {
		t.Fatalf("ProducerAttemptID = %q", r.ProducerAttemptID)
	}
	if got.ContainerName != "main" {
		t.Fatalf("ContainerName = %q", got.ContainerName)
	}
}
