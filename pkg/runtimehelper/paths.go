package runtimehelper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func secureOutputPath(outputRoot, declaredPath string) (string, error) {
	if declaredPath == "" {
		return "", fmt.Errorf("empty output path")
	}
	if filepath.IsAbs(declaredPath) {
		return "", fmt.Errorf("absolute output path is not allowed")
	}
	if !filepath.IsLocal(declaredPath) {
		return "", fmt.Errorf("non-local output path is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(outputRoot, declaredPath))
	rel, err := filepath.Rel(outputRoot, candidate)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("output path escapes output root")
	}
	return candidate, nil
}

func validateNodeLocalSourcePath(cfg Config, sourcePath string) error {
	if !filepath.IsAbs(sourcePath) {
		return markPolicyRejection(fmt.Errorf("node-local source path must be absolute: %q", sourcePath))
	}
	root := strings.TrimSpace(cfg.NodeLocalArtifactRoot)
	if root == "" {
		return markPolicyRejection(fmt.Errorf("node-local artifact root is required for local_reuse"))
	}
	cleanedRoot := filepath.Clean(root)
	cleanedPath := filepath.Clean(sourcePath)
	rel, err := filepath.Rel(cleanedRoot, cleanedPath)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return markPolicyRejection(fmt.Errorf("node-local source path %q is outside allowed root %q", sourcePath, cleanedRoot))
	}
	info, err := os.Lstat(cleanedPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return markPolicyRejection(fmt.Errorf("node-local source path %q must not be a symlink", sourcePath))
	}
	realRoot, err := filepath.EvalSymlinks(cleanedRoot)
	if err != nil {
		return fmt.Errorf("resolve node-local artifact root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(cleanedPath)
	if err != nil {
		return fmt.Errorf("resolve node-local source path: %w", err)
	}
	realRel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return err
	}
	if realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(os.PathSeparator)) {
		return markPolicyRejection(fmt.Errorf("node-local source path %q escapes resolved root %q", sourcePath, realRoot))
	}
	return nil
}

func materializedInputPath(workRoot string, input InputSpec) (string, error) {
	workRoot = effectiveWorkRoot(workRoot)
	if strings.TrimSpace(input.LocalPath) != "" {
		return secureInputMaterializedPath(workRoot, input.LocalPath)
	}
	return secureInputMaterializedPath(workRoot, filepath.Join("inputs", strings.ToLower(safeInputName(input.Name))))
}

func secureInputMaterializedPath(workRoot, relativePath string) (string, error) {
	if relativePath == "" {
		return "", fmt.Errorf("empty materialized path")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("absolute materialized path is not allowed")
	}
	if !filepath.IsLocal(relativePath) {
		return "", fmt.Errorf("non-local materialized path is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(workRoot, relativePath))
	rel, err := filepath.Rel(workRoot, candidate)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("materialized path escapes work root")
	}
	inputsPrefix := "inputs" + string(os.PathSeparator)
	if rel == "inputs" || !strings.HasPrefix(rel, inputsPrefix) {
		return "", fmt.Errorf("materialized path must stay under inputs/")
	}
	if err := ensureNoSymlinkPathPrefix(workRoot, rel); err != nil {
		return "", err
	}
	return candidate, nil
}

func ensureNoSymlinkPathPrefix(workRoot, relativePath string) error {
	current := filepath.Clean(workRoot)
	parts := strings.Split(relativePath, string(os.PathSeparator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				if i == len(parts)-1 {
					return nil
				}
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("materialized path traverses symlink %q", current)
		}
	}
	return nil
}

func safeInputName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "input"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "input"
	}
	const maxLen = 64
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
		if out == "" {
			return "input"
		}
	}
	return out
}

func effectiveWorkRoot(workRoot string) string {
	return FirstNonEmpty(workRoot, "/work")
}
