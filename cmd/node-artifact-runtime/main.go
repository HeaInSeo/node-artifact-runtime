package main

import (
	"context"
	"flag"
	"os"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
	"github.com/HeaInSeo/node-artifact-runtime/pkg/runtimehelper"
)

func main() {
	runID := flag.String("run-id", os.Getenv("JUMI_RUN_ID"), "run identifier")
	sampleRunID := flag.String("sample-run-id", os.Getenv("JUMI_SAMPLE_RUN_ID"), "sample run identifier")
	nodeID := flag.String("node-id", os.Getenv("JUMI_NODE_ID"), "node identifier")
	outputNames := flag.String("output-names", os.Getenv("JUMI_OUTPUT_NAMES"), "comma-separated output names")
	outputRoot := flag.String("output-root", "/out", "output root path")
	manifestPath := flag.String("manifest-path", firstNonEmpty(os.Getenv("JUMI_OUTPUT_MANIFEST_PATH"), provenance.DefaultArtifactsManifestPath), "artifacts manifest path")
	terminationLogPath := flag.String("termination-log-path", "/dev/termination-log", "termination log path")
	flag.Parse()

	os.Exit(runtimehelper.Run(context.Background(), runtimehelper.Config{
		RunID:              *runID,
		SampleRunID:        *sampleRunID,
		NodeID:             *nodeID,
		OutputNames:        runtimehelper.ParseOutputNames(*outputNames),
		OutputRoot:         *outputRoot,
		ManifestPath:       *manifestPath,
		TerminationLogPath: *terminationLogPath,
		Command:            flag.Args(),
	}))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
