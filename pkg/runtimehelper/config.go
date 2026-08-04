package runtimehelper

import (
	"fmt"
	"io"
	"time"
)

type OutputSpec struct {
	Name     string
	Path     string
	Required bool
	Type     string
}

type InputSpec struct {
	Name                string
	URI                 string
	ExpectedDigest      string
	ExpectedSizeBytes   int64
	MaterializationMode string
	NodeLocalPath       string
	LocalPath           string
}

type partialInputSpec struct {
	name                string
	uri                 string
	expectedDigest      string
	expectedSizeBytes   string
	materializationMode string
	nodeLocalPath       string
	localPath           string
}

type TerminationSummary struct {
	Status       string `json:"status"`
	ExitCode     int    `json:"exitCode"`
	RunID        string `json:"runId,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	ManifestPath string `json:"manifestPath,omitempty"`
	Message      string `json:"message,omitempty"`
	// Reason is a structured, stable classification of the failure, added
	// additively alongside the free-form Status/Message so downstream
	// consumers (e.g. JUMI backend) can map to their own reserved reasons
	// without parsing message strings. Empty when a failure has no structured
	// reason in the current stable set. See MaterializeReason for the v0.1
	// materialization reason set.
	Reason string `json:"reason,omitempty"`
}

// Config describes the runtime-side artifact helper contract executed inside
// the DAG node runtime container after wrapping the user command.
type Config struct {
	RunID                 string
	SampleRunID           string
	NodeID                string
	AttemptID             string
	ContainerName         string
	OutputNames           []string
	Inputs                []InputSpec
	WorkRoot              string
	NodeLocalArtifactRoot string
	HTTPAllowedHosts      []string
	HTTPAllowAny          bool
	HTTPTimeout           time.Duration
	HTTPMaxRedirects      int
	HTTPMaxInputBytes     int64
	Outputs               []OutputSpec
	OutputRoot            string
	ManifestPath          string
	TerminationLogPath    string
	AllowDirectoryOutput  bool
	InspectOnSuccessOnly  bool
	// CommandExitCode holds the exit code of the user command when Inspect is
	// called standalone (not via Run). Used with InspectOnSuccessOnly.
	CommandExitCode int
	// RunTimeout is a wall-clock limit for the entire Run lifecycle
	// (materialize + execute + inspect). Zero means no timeout.
	RunTimeout time.Duration
	// ShutdownGracePeriod is how long nan waits after forwarding SIGTERM or an
	// external shutdown signal before escalating the user process group to SIGKILL.
	ShutdownGracePeriod time.Duration
	Command             []string
	Stdout              io.Writer
	Stderr              io.Writer
}

func (c Config) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("%w: runID is required", errInvalidConfig)
	}
	if c.NodeID == "" {
		return fmt.Errorf("%w: nodeID is required", errInvalidConfig)
	}
	if c.OutputRoot == "" {
		return fmt.Errorf("%w: outputRoot is required", errInvalidConfig)
	}
	if c.ManifestPath == "" {
		return fmt.Errorf("%w: manifestPath is required", errInvalidConfig)
	}
	for _, input := range c.Inputs {
		if input.ExpectedSizeBytes < 0 {
			return fmt.Errorf("%w: input %s expected size must be >= 0", errInvalidConfig, input.Name)
		}
	}
	return nil
}
