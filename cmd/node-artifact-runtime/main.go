package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/contract"
	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
	"github.com/HeaInSeo/node-artifact-runtime/pkg/runtimehelper"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(runCommand(nil))
	}
	switch args[0] {
	case "run":
		os.Exit(runCommand(args[1:]))
	case "inspect":
		os.Exit(inspectCommand(args[1:]))
	case "version":
		fmt.Println(runtimehelper.Version)
		os.Exit(0)
	default:
		os.Exit(runCommand(args))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	contractPath := fs.String("contract", "", "path to node contract JSON")
	runID := fs.String("run-id", os.Getenv("JUMI_RUN_ID"), "run identifier")
	sampleRunID := fs.String("sample-run-id", os.Getenv("JUMI_SAMPLE_RUN_ID"), "sample run identifier")
	nodeID := fs.String("node-id", os.Getenv("JUMI_NODE_ID"), "node identifier")
	attemptID := fs.String("attempt-id", os.Getenv("JUMI_ATTEMPT_ID"), "attempt identifier")
	outputNames := fs.String("output-names", os.Getenv("JUMI_OUTPUT_NAMES"), "comma-separated output names")
	workRoot := fs.String("work-root", firstNonEmpty(os.Getenv("JUMI_WORK_ROOT"), "/work"), "work root path")
	outputRoot := fs.String("output-root", firstNonEmpty(os.Getenv("JUMI_OUTPUT_ROOT"), "/out"), "output root path")
	manifestPath := fs.String("manifest-path", firstNonEmpty(os.Getenv("JUMI_OUTPUT_MANIFEST_PATH"), provenance.DefaultArtifactsManifestPath), "artifacts manifest path")
	terminationLogPath := fs.String("termination-log-path", "/dev/termination-log", "termination log path")
	if err := fs.Parse(args); err != nil {
		return runtimehelper.ExitInvalidConfig
	}
	cfg, err := buildConfig(*contractPath, *runID, *sampleRunID, *nodeID, *attemptID, *outputNames, *workRoot, *outputRoot, *manifestPath, *terminationLogPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runtimehelper.ExitInvalidConfig
	}
	cfg.Command = fs.Args()
	return runtimehelper.Run(context.Background(), cfg)
}

func inspectCommand(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	contractPath := fs.String("contract", "", "path to node contract JSON")
	runID := fs.String("run-id", os.Getenv("JUMI_RUN_ID"), "run identifier")
	sampleRunID := fs.String("sample-run-id", os.Getenv("JUMI_SAMPLE_RUN_ID"), "sample run identifier")
	nodeID := fs.String("node-id", os.Getenv("JUMI_NODE_ID"), "node identifier")
	attemptID := fs.String("attempt-id", os.Getenv("JUMI_ATTEMPT_ID"), "attempt identifier")
	outputNames := fs.String("output-names", os.Getenv("JUMI_OUTPUT_NAMES"), "comma-separated output names")
	workRoot := fs.String("work-root", firstNonEmpty(os.Getenv("JUMI_WORK_ROOT"), "/work"), "work root path")
	outputRoot := fs.String("output-root", firstNonEmpty(os.Getenv("JUMI_OUTPUT_ROOT"), "/out"), "output root path")
	manifestPath := fs.String("manifest-path", firstNonEmpty(os.Getenv("JUMI_OUTPUT_MANIFEST_PATH"), provenance.DefaultArtifactsManifestPath), "artifacts manifest path")
	terminationLogPath := fs.String("termination-log-path", "/dev/termination-log", "termination log path")
	if err := fs.Parse(args); err != nil {
		return runtimehelper.ExitInvalidConfig
	}
	cfg, err := buildConfig(*contractPath, *runID, *sampleRunID, *nodeID, *attemptID, *outputNames, *workRoot, *outputRoot, *manifestPath, *terminationLogPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return runtimehelper.ExitInvalidConfig
	}
	return runtimehelper.Inspect(cfg)
}

func buildConfig(contractPath, runID, sampleRunID, nodeID, attemptID, outputNames, workRoot, outputRoot, manifestPath, terminationLogPath string) (runtimehelper.Config, error) {
	nodeLocalArtifactRoot := os.Getenv("JUMI_NODE_LOCAL_ARTIFACT_ROOT")
	httpAllowedHosts := parseCommaSeparated(os.Getenv("JUMI_ALLOWED_HTTP_SOURCE_HOSTS"))
	httpAllowAny := parseBoolEnv(os.Getenv("JUMI_ALLOW_ANY_HTTP_SOURCE"))
	httpTimeout, err := parsePositiveDurationEnv("JUMI_HTTP_SOURCE_TIMEOUT")
	if err != nil {
		return runtimehelper.Config{}, err
	}
	httpMaxRedirects := parsePositiveInt(os.Getenv("JUMI_HTTP_SOURCE_MAX_REDIRECTS"))
	httpMaxInputBytes := parsePositiveInt64(os.Getenv("JUMI_HTTP_SOURCE_MAX_INPUT_BYTES"))
	if contractPath != "" {
		c, err := contract.Load(contractPath)
		if err != nil {
			return runtimehelper.Config{}, err
		}
		cfg := runtimehelper.ConfigFromContract(c)
		cfg.TerminationLogPath = terminationLogPath
		cfg.NodeLocalArtifactRoot = nodeLocalArtifactRoot
		cfg.HTTPAllowedHosts = httpAllowedHosts
		cfg.HTTPAllowAny = httpAllowAny
		cfg.HTTPTimeout = httpTimeout
		cfg.HTTPMaxRedirects = httpMaxRedirects
		cfg.HTTPMaxInputBytes = httpMaxInputBytes
		cfg.Inputs, err = runtimehelper.ParseInputSpecsFromEnv(os.Environ(), cfg.WorkRoot)
		if err != nil {
			return runtimehelper.Config{}, err
		}
		return cfg, nil
	}
	inputs, err := runtimehelper.ParseInputSpecsFromEnv(os.Environ(), workRoot)
	if err != nil {
		return runtimehelper.Config{}, err
	}
	return runtimehelper.Config{
		RunID:                 runID,
		SampleRunID:           sampleRunID,
		NodeID:                nodeID,
		AttemptID:             attemptID,
		Inputs:                inputs,
		OutputNames:           runtimehelper.ParseOutputNames(outputNames),
		WorkRoot:              workRoot,
		NodeLocalArtifactRoot: nodeLocalArtifactRoot,
		HTTPAllowedHosts:      httpAllowedHosts,
		HTTPAllowAny:          httpAllowAny,
		HTTPTimeout:           httpTimeout,
		HTTPMaxRedirects:      httpMaxRedirects,
		HTTPMaxInputBytes:     httpMaxInputBytes,
		OutputRoot:            outputRoot,
		ManifestPath:          manifestPath,
		TerminationLogPath:    terminationLogPath,
	}, nil
}

func parseCommaSeparated(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseBoolEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parsePositiveInt(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	if value > 0 {
		return value
	}
	return 0
}

func parsePositiveInt64(raw string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if value > 0 {
		return value
	}
	return 0
}

func parsePositiveDurationEnv(key string) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("invalid %s: must be > 0", key)
	}
	return value, nil
}
