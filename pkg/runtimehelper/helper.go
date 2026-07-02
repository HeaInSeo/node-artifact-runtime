package runtimehelper

import (
	"errors"
	"time"
)

const Version = "v0.3.2"

var (
	errInvalidConfig         = errors.New("invalid config")
	errInvalidOutputPath     = errors.New("invalid output path")
	errMissingRequiredOutput = errors.New("missing required output")
	errUnsupportedOutputType = errors.New("unsupported output type")
	errInspectFailed         = errors.New("inspect failed")
	errMaterializeFailed     = errors.New("materialize failed")
	errManifestWriteFailed   = errors.New("manifest write failed")
	errProcessGroupNotClean  = errors.New("process group not clean")
	errSubreaperSetupFailed  = errors.New("subreaper setup failed")
)

const (
	ExitSuccess               = 0
	ExitGenericError          = 1
	ExitInvalidConfig         = 2
	ExitInvalidCommand        = 64
	ExitInvalidOutputPath     = 65
	ExitMissingRequiredOutput = 66
	ExitUnsupportedOutputType = 67
	ExitMaterializeFailed     = 69
	ExitInspectFailed         = 70
	ExitManifestWriteFailed   = 74
	ExitTimeout               = 75
)

const DefaultShutdownGracePeriod = 25 * time.Second

const (
	linuxPRSetChildSubreaper = 36
	processGroupPollInterval = 10 * time.Millisecond
	processGroupKillWait     = 2 * time.Second
)
