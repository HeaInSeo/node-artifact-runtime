package main

import (
	"context"
	"flag"
	"fmt"
	"os"

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
	if contractPath != "" {
		c, err := contract.Load(contractPath)
		if err != nil {
			return runtimehelper.Config{}, err
		}
		cfg := runtimehelper.ConfigFromContract(c)
		cfg.TerminationLogPath = terminationLogPath
		cfg.NodeLocalArtifactRoot = nodeLocalArtifactRoot
		cfg.Inputs = runtimehelper.ParseInputSpecsFromEnv(os.Environ(), cfg.WorkRoot)
		return cfg, nil
	}
	return runtimehelper.Config{
		RunID:                 runID,
		SampleRunID:           sampleRunID,
		NodeID:                nodeID,
		AttemptID:             attemptID,
		Inputs:                runtimehelper.ParseInputSpecsFromEnv(os.Environ(), workRoot),
		OutputNames:           runtimehelper.ParseOutputNames(outputNames),
		WorkRoot:              workRoot,
		NodeLocalArtifactRoot: nodeLocalArtifactRoot,
		OutputRoot:            outputRoot,
		ManifestPath:          manifestPath,
		TerminationLogPath:    terminationLogPath,
	}, nil
}
