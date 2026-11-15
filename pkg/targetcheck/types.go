// SPDX-License-Identifier: Apache-2.0

package targetcheck

import "github.com/firmdura/targetcheck/internal/model"

type (
	Spec               = model.Spec
	Options            = model.Options
	Target             = model.Target
	TargetKind         = model.TargetKind
	Command            = model.Command
	Variant            = model.Variant
	Variants           = model.Variants
	Test               = model.Test
	Expectation        = model.Expectation
	Report             = model.Report
	VariantReport      = model.VariantReport
	VariantName        = model.VariantName
	Check              = model.Check
	CheckKind          = model.CheckKind
	CheckStatus        = model.CheckStatus
	Outcome            = model.Outcome
	ErrorKind          = model.ErrorKind
	FailureKind        = model.FailureKind
	Failure            = model.Failure
	RunError           = model.RunError
	FileIdentity       = model.FileIdentity
	ManifestIdentity   = model.ManifestIdentity
	ArtifactIdentity   = model.ArtifactIdentity
	ELFIdentity        = model.ELFIdentity
	ToolIdentity       = model.ToolIdentity
	TargetInfo         = model.TargetInfo
	ToolVersion        = model.ToolVersion
	VariantCoverage    = model.VariantCoverage
	Capture            = model.Capture
	ProcessObservation = model.ProcessObservation
)

const (
	SchemaVersion       = model.SchemaVersion
	TargetNative        = model.TargetNative
	TargetQEMUARM       = model.TargetQEMUARM
	Baseline            = model.Baseline
	Candidate           = model.Candidate
	CheckBuild          = model.CheckBuild
	CheckTest           = model.CheckTest
	CheckMatched        = model.CheckMatched
	CheckMismatch       = model.CheckMismatch
	CheckNotRun         = model.CheckNotRun
	CheckError          = model.CheckError
	OutcomeMatched      = model.OutcomeMatched
	OutcomeMismatch     = model.OutcomeMismatch
	OutcomeError        = model.OutcomeError
	OutcomeCancelled    = model.OutcomeCancelled
	ErrorInvalidInput   = model.ErrorInvalidInput
	ErrorInfrastructure = model.ErrorInfrastructure
	ErrorCancelled      = model.ErrorCancelled
	FailureInvalidInput = model.FailureInvalidInput
	FailureMissingTool  = model.FailureMissingTool
	FailureBuild        = model.FailureBuild
	FailureLaunch       = model.FailureLaunch
	FailureSignal       = model.FailureSignal
	FailureTimeout      = model.FailureTimeout
	FailureCancelled    = model.FailureCancelled
	FailureOutputLimit  = model.FailureOutputLimit
	FailureIdentity     = model.FailureIdentity
	FailureArtifact     = model.FailureArtifact
	FailureIO           = model.FailureIO
	FailureNotRun       = model.FailureNotRun
)
