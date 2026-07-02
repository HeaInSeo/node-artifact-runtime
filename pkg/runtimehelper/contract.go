package runtimehelper

import "github.com/HeaInSeo/node-artifact-runtime/pkg/contract"

func ConfigFromContract(c contract.NodeContract) Config {
	inputs := make([]InputSpec, 0, len(c.Inputs))
	for _, input := range c.Inputs {
		inputs = append(inputs, InputSpec{
			Name:                input.Name,
			URI:                 input.URI,
			ExpectedDigest:      input.ExpectedDigest,
			ExpectedSizeBytes:   input.ExpectedSizeBytes,
			MaterializationMode: input.MaterializationMode,
			NodeLocalPath:       input.NodeLocalPath,
			LocalPath:           input.LocalPath,
		})
	}
	outputs := make([]OutputSpec, 0, len(c.Outputs))
	for _, output := range c.Outputs {
		required := output.Required
		if !output.Required && c.Runtime.FailOnMissingRequiredOutput {
			required = true
		}
		outputs = append(outputs, OutputSpec{
			Name:     output.Name,
			Path:     output.Path,
			Required: required,
			Type:     FirstNonEmpty(output.Type, "file"),
		})
	}
	return Config{
		RunID:                c.RunID,
		SampleRunID:          c.SampleRunID,
		NodeID:               c.NodeID,
		AttemptID:            c.AttemptID,
		ContainerName:        c.ContainerName,
		Inputs:               inputs,
		Outputs:              outputs,
		WorkRoot:             FirstNonEmpty(c.Paths.WorkRoot, "/work"),
		OutputRoot:           c.Paths.OutputRoot,
		ManifestPath:         c.Paths.ManifestPath,
		AllowDirectoryOutput: c.Runtime.AllowDirectoryOutput,
		InspectOnSuccessOnly: c.Runtime.InspectOnSuccessOnly,
	}
}
