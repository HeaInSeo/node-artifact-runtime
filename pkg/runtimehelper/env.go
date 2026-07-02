package runtimehelper

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func ParseInputSpecsFromEnv(env []string, workRoot string) ([]InputSpec, error) {
	byBase := map[string]*partialInputSpec{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "JUMI_INPUT_") {
			continue
		}
		switch {
		case strings.HasSuffix(key, "_URI"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_URI")
			p := ensurePartial(byBase, base)
			p.uri = value
		case strings.HasSuffix(key, "_EXPECTED_DIGEST"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_EXPECTED_DIGEST")
			p := ensurePartial(byBase, base)
			p.expectedDigest = value
		case strings.HasSuffix(key, "_EXPECTED_SIZE_BYTES"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_EXPECTED_SIZE_BYTES")
			p := ensurePartial(byBase, base)
			p.expectedSizeBytes = value
		case strings.HasSuffix(key, "_MATERIALIZATION_MODE"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_MATERIALIZATION_MODE")
			p := ensurePartial(byBase, base)
			p.materializationMode = value
		case strings.HasSuffix(key, "_NODE_LOCAL_PATH"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_NODE_LOCAL_PATH")
			p := ensurePartial(byBase, base)
			p.nodeLocalPath = value
		case strings.HasSuffix(key, "_LOCAL_PATH") && !strings.HasSuffix(key, "_NODE_LOCAL_PATH"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_LOCAL_PATH")
			p := ensurePartial(byBase, base)
			p.localPath = value
		}
	}
	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	inputs := make([]InputSpec, 0, len(bases))
	for _, base := range bases {
		p := byBase[base]
		if strings.TrimSpace(p.materializationMode) == "" {
			continue
		}
		expectedSizeBytes := int64(0)
		if strings.TrimSpace(p.expectedSizeBytes) != "" {
			parsed, err := strconv.ParseInt(strings.TrimSpace(p.expectedSizeBytes), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid expected size for input %s: %v", errInvalidConfig, base, err)
			}
			if parsed <= 0 {
				return nil, fmt.Errorf("%w: invalid expected size for input %s: must be > 0", errInvalidConfig, base)
			}
			expectedSizeBytes = parsed
		}
		inputs = append(inputs, InputSpec{
			Name:                strings.ToLower(base),
			URI:                 p.uri,
			ExpectedDigest:      p.expectedDigest,
			ExpectedSizeBytes:   expectedSizeBytes,
			MaterializationMode: p.materializationMode,
			NodeLocalPath:       p.nodeLocalPath,
			LocalPath:           FirstNonEmpty(p.localPath, filepath.Join("inputs", strings.ToLower(base))),
		})
	}
	return inputs, nil
}

func ensurePartial(partials map[string]*partialInputSpec, base string) *partialInputSpec {
	if partials[base] == nil {
		partials[base] = &partialInputSpec{name: base}
	}
	return partials[base]
}

func (c Config) commandEnv() ([]string, error) {
	env := append([]string{}, os.Environ()...)
	for _, input := range c.Inputs {
		localPath, err := materializedInputPath(effectiveWorkRoot(c.WorkRoot), input)
		if err != nil {
			return nil, fmt.Errorf("env for input %s: %w", input.Name, err)
		}
		keyBase := strings.ToUpper(strings.ReplaceAll(safeInputName(input.Name), "-", "_"))
		env = append(env, "JUMI_INPUT_"+keyBase+"_LOCAL_PATH="+localPath)
	}
	return env, nil
}
