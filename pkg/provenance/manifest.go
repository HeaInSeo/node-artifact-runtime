package provenance

import (
	"encoding/json"
	"fmt"
)

const DefaultArtifactsManifestPath = "/out/_meta/artifacts.manifest.json"

type ArtifactManifest struct {
	Artifacts []ArtifactRecord `json:"artifacts"`
}

type ArtifactRecord struct {
	OutputName string `json:"outputName"`
	URI        string `json:"uri,omitempty"`
	Digest     string `json:"digest,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

func ParseArtifactManifest(data []byte) (ArtifactManifest, error) {
	var manifest ArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ArtifactManifest{}, err
	}
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if artifact.OutputName == "" {
			return ArtifactManifest{}, fmt.Errorf("artifact outputName is required")
		}
		if _, ok := seen[artifact.OutputName]; ok {
			return ArtifactManifest{}, fmt.Errorf("duplicate artifact outputName %q", artifact.OutputName)
		}
		seen[artifact.OutputName] = struct{}{}
	}
	return manifest, nil
}

func (m ArtifactManifest) ByOutputName(name string) (ArtifactRecord, bool) {
	for _, artifact := range m.Artifacts {
		if artifact.OutputName == name {
			return artifact, true
		}
	}
	return ArtifactRecord{}, false
}
