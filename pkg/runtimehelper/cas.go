package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

func promoteOutputToNodeLocalCAS(ctx context.Context, cfg Config, output OutputSpec, sourcePath string) (*provenance.NodeLocalLocation, string, int64, error) {
	root := filepath.Clean(cfg.NodeLocalArtifactRoot)
	casDir := filepath.Join(root, "cas", "sha256")
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(casDir, 0o750); err != nil {
		return nil, "", 0, fmt.Errorf("%w: mkdir CAS dir for output %s: %v", errInspectFailed, output.Name, err)
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, "", 0, fmt.Errorf("%w: mkdir tmp dir for output %s: %v", errInspectFailed, output.Name, err)
	}
	tmpFile, err := os.CreateTemp(tmpDir, fmt.Sprintf("%s-%s-%s-%s-", cfg.RunID, cfg.NodeID, cfg.AttemptID, output.Name))
	if err != nil {
		return nil, "", 0, fmt.Errorf("%w: create temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: open output %s for CAS promotion: %v", errInspectFailed, output.Name, err)
	}
	defer func() {
		_ = sourceFile.Close()
	}()

	hash := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(tmpFile, hash), sourceFile)
	if err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: copy output %s to temp CAS: %v", errInspectFailed, output.Name, err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: sync temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, "", 0, fmt.Errorf("%w: close temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}

	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if hexDigest == "" || hexDigest == digest {
		return nil, "", 0, fmt.Errorf("%w: output %s has unsupported digest %q", errInspectFailed, output.Name, digest)
	}
	finalPath := filepath.Join(casDir, hexDigest)
	if ok, err := verifyExistingCASArtifact(finalPath, digest); err != nil {
		return nil, "", 0, err
	} else if ok {
		return &provenance.NodeLocalLocation{Path: finalPath}, digest, size, nil
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, "", 0, fmt.Errorf("%w: move promoted output %s into CAS: %v", errInspectFailed, output.Name, err)
	}
	return &provenance.NodeLocalLocation{Path: finalPath}, digest, size, nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return written, nil
			}
			return written, er
		}
	}
}

func verifyExistingCASArtifact(path, expectedDigest string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: stat existing CAS artifact: %v", errInspectFailed, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: existing CAS artifact is not a regular file", errInspectFailed)
	}
	actual, err := sha256Digest(path)
	if err != nil {
		return false, fmt.Errorf("%w: hash existing CAS artifact: %v", errInspectFailed, err)
	}
	if actual != expectedDigest {
		return false, fmt.Errorf("%w: existing CAS artifact digest mismatch: got %s want %s", errInspectFailed, actual, expectedDigest)
	}
	return true, nil
}

func sha256Digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
