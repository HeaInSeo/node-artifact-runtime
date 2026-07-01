package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

func EmitArtifacts(cfg Config) error {
	return EmitArtifactsContext(context.Background(), cfg)
}

func EmitArtifactsContext(ctx context.Context, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	outputs := cfg.effectiveOutputs()
	manifest := provenance.ArtifactManifest{
		SchemaVersion: provenance.ArtifactManifestSchemaVersion,
		RunID:         cfg.RunID,
		SampleRunID:   cfg.SampleRunID,
		NodeID:        cfg.NodeID,
		AttemptID:     cfg.AttemptID,
		ContainerName: cfg.ContainerName,
		NanVersion:    Version,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		OutputRoot:    cfg.OutputRoot,
		Artifacts:     make([]provenance.ArtifactRecord, 0, len(outputs)),
	}
	for _, output := range outputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, ok, err := buildArtifactRecordContext(ctx, cfg, output)
		if err != nil {
			return err
		}
		if ok {
			manifest.Artifacts = append(manifest.Artifacts, record)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("%w: marshal artifacts manifest: %v", errManifestWriteFailed, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := atomicWriteFileContext(ctx, cfg.ManifestPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: write manifest: %v", errManifestWriteFailed, err)
	}
	return nil
}

func buildArtifactRecord(cfg Config, output OutputSpec) (provenance.ArtifactRecord, bool, error) {
	return buildArtifactRecordContext(context.Background(), cfg, output)
}

func buildArtifactRecordContext(ctx context.Context, cfg Config, output OutputSpec) (provenance.ArtifactRecord, bool, error) {
	path, err := secureOutputPath(cfg.OutputRoot, output.Path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s: %v", errInvalidOutputPath, output.Name, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if output.Required {
				return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s missing", errMissingRequiredOutput, output.Name)
			}
			return provenance.ArtifactRecord{}, false, nil
		}
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: stat output %s: %v", errInspectFailed, output.Name, err)
	}
	// Re-validate after symlink resolution: the declared path may be a symlink
	// whose target resolves outside the output root.
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: stat output %s: %v", errInspectFailed, output.Name, err)
	}
	cleanRoot := filepath.Clean(cfg.OutputRoot)
	rel, err := filepath.Rel(cleanRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s symlink escapes output root", errInvalidOutputPath, output.Name)
	}
	path = realPath
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is a directory; directory artifacts are not supported", errUnsupportedOutputType, output.Name)
		}
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is not a regular file", errUnsupportedOutputType, output.Name)
	}
	uri := fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", cfg.RunID, cfg.NodeID, output.Name)
	if cfg.AttemptID != "" {
		uri = fmt.Sprintf("jumi://runs/%s/nodes/%s/attempts/%s/outputs/%s", cfg.RunID, cfg.NodeID, cfg.AttemptID, output.Name)
	}
	logicalURI := fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", cfg.RunID, cfg.NodeID, output.Name)

	var (
		digest   string
		size     int64
		location *provenance.NodeLocalLocation
	)
	// Re-resolve symlinks immediately before opening to shrink the TOCTOU window.
	// If the resolved path changed since the earlier check, a symlink was swapped
	// between validation and use; reject to prevent escape.
	if recheckPath, rerr := filepath.EvalSymlinks(path); rerr != nil || recheckPath != path {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s path changed between validation and open (possible TOCTOU)", errInvalidOutputPath, output.Name)
	}
	if strings.TrimSpace(cfg.NodeLocalArtifactRoot) != "" {
		location, digest, size, err = promoteOutputToNodeLocalCAS(ctx, cfg, output, path)
		if err != nil {
			return provenance.ArtifactRecord{}, false, err
		}
	} else {
		// #nosec G304 -- path is validated above and resolves under the container output root.
		f, err := os.Open(path)
		if err != nil {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: open output %s: %v", errInspectFailed, output.Name, err)
		}
		defer func() { _ = f.Close() }()
		hash := sha256.New()
		size, err = copyWithContext(ctx, hash, f)
		if err != nil {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: hash output %s: %v", errInspectFailed, output.Name, err)
		}
		digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	}
	record := provenance.ArtifactRecord{
		OutputName:        output.Name,
		DeclaredPath:      output.Path,
		AbsolutePath:      path,
		URI:               uri,
		LogicalURI:        logicalURI,
		Digest:            digest,
		SizeBytes:         size,
		Type:              FirstNonEmpty(output.Type, "file"),
		ProducerAttemptID: cfg.AttemptID,
	}
	if location != nil {
		record.Locations = []provenance.ArtifactLocation{{NodeLocal: location}}
	}
	return record, true, nil
}
