package contract

import (
	"encoding/json"
	"fmt"
	"os"
)

const NodeContractSchemaVersion = "nan.nodeContract.v1"

type NodeContract struct {
	SchemaVersion string           `json:"schemaVersion"`
	RunID         string           `json:"runId"`
	SampleRunID   string           `json:"sampleRunId,omitempty"`
	NodeID        string           `json:"nodeId"`
	AttemptID     string           `json:"attemptId,omitempty"`
	ContainerName string           `json:"containerName,omitempty"`
	Paths         ContractPaths    `json:"paths"`
	Inputs        []ContractInput  `json:"inputs,omitempty"`
	Outputs       []ContractOutput `json:"outputs,omitempty"`
	Runtime       RuntimePolicy    `json:"runtime,omitempty"`
}

type ContractPaths struct {
	InputRoot    string `json:"inputRoot,omitempty"`
	WorkRoot     string `json:"workRoot,omitempty"`
	OutputRoot   string `json:"outputRoot"`
	ManifestPath string `json:"manifestPath"`
}

type ContractOutput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Required bool   `json:"required,omitempty"`
	Type     string `json:"type,omitempty"`
}

type ContractInput struct {
	Name                string `json:"name"`
	URI                 string `json:"uri,omitempty"`
	ExpectedDigest      string `json:"expectedDigest,omitempty"`
	ExpectedSizeBytes   int64  `json:"expectedSizeBytes,omitempty"`
	MaterializationMode string `json:"materializationMode,omitempty"`
	NodeLocalPath       string `json:"nodeLocalPath,omitempty"`
	LocalPath           string `json:"localPath,omitempty"`
}

type RuntimePolicy struct {
	InspectOnSuccessOnly        bool `json:"inspectOnSuccessOnly,omitempty"`
	FailOnMissingRequiredOutput bool `json:"failOnMissingRequiredOutput,omitempty"`
	AllowDirectoryOutput        bool `json:"allowDirectoryOutput,omitempty"`
}

func Load(path string) (NodeContract, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return NodeContract{}, fmt.Errorf("read contract: %w", err)
	}
	var c NodeContract
	if err := json.Unmarshal(raw, &c); err != nil {
		return NodeContract{}, fmt.Errorf("decode contract: %w", err)
	}
	if c.SchemaVersion == "" {
		c.SchemaVersion = NodeContractSchemaVersion
	}
	if c.SchemaVersion != NodeContractSchemaVersion {
		return NodeContract{}, fmt.Errorf("unsupported schemaVersion %q", c.SchemaVersion)
	}
	if err := c.Validate(); err != nil {
		return NodeContract{}, err
	}
	return c, nil
}

func (c NodeContract) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("runId is required")
	}
	if c.NodeID == "" {
		return fmt.Errorf("nodeId is required")
	}
	if c.Paths.OutputRoot == "" {
		return fmt.Errorf("paths.outputRoot is required")
	}
	if c.Paths.ManifestPath == "" {
		return fmt.Errorf("paths.manifestPath is required")
	}
	for i, output := range c.Outputs {
		if output.Name == "" {
			return fmt.Errorf("outputs[%d].name is required", i)
		}
		if output.Path == "" {
			return fmt.Errorf("outputs[%d].path is required", i)
		}
	}
	for i, input := range c.Inputs {
		if input.Name == "" {
			return fmt.Errorf("inputs[%d].name is required", i)
		}
		if input.ExpectedSizeBytes < 0 {
			return fmt.Errorf("inputs[%d].expectedSizeBytes must be >= 0", i)
		}
	}
	return nil
}
