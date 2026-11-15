// SPDX-License-Identifier: Apache-2.0

// Package model defines the versioned contracts shared by TargetCheck components.
// It performs no command execution, filesystem inspection, or report rendering.
package model

import (
	"context"
	"io"
)

const (
	SchemaVersion     = 1
	MaxManifestBytes  = 1 << 20
	MaxTests          = 256
	MaxBuildSteps     = 64
	MaxCaptureBytes   = 1 << 20
	MinTimeoutSeconds = 1
	MaxTimeoutSeconds = 3600
)

type TargetKind string

const (
	TargetNative  TargetKind = "native"
	TargetQEMUARM TargetKind = "qemu-arm"
)

// Target specifies execution of tests; builds always run on the host.
type Target struct {
	Kind       TargetKind `json:"kind"`
	Executable string     `json:"executable,omitempty"`
	Sysroot    string     `json:"sysroot,omitempty"`
}

type Command struct {
	Argv           []string          `json:"argv"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

type Variant struct {
	CWD       string    `json:"cwd"`
	Build     []Command `json:"build"`
	Inputs    []string  `json:"inputs"`
	Artifacts []string  `json:"artifacts"`
}

type Variants struct {
	Baseline  Variant `json:"baseline"`
	Candidate Variant `json:"candidate"`
}

// Expectation requires the exact normal exit and every nonempty literal marker.
// Signals and infrastructure failures never satisfy an expectation.
type Expectation struct {
	ExitCode       int    `json:"exit_code"`
	StdoutContains string `json:"stdout_contains,omitempty"`
	StderrContains string `json:"stderr_contains,omitempty"`
}

type Test struct {
	ID              string      `json:"id"`
	Argv            []string    `json:"argv"`
	TimeoutSeconds  int         `json:"timeout_seconds"`
	BaselineExpect  Expectation `json:"baseline_expect"`
	CandidateExpect Expectation `json:"candidate_expect"`
}

type Spec struct {
	SchemaVersion int      `json:"schema_version"`
	Target        Target   `json:"target"`
	Variants      Variants `json:"variants"`
	TestInputs    []string `json:"test_inputs"`
	Tests         []Test   `json:"tests"`
	// ManifestJSON retains original Load bytes for exact input identity. Prepare
	// must verify they decode to this Spec before using them; changed/manual Specs
	// use a canonical JSON identity instead. Never execute values from these bytes.
	ManifestJSON []byte `json:"-"`
}

// Options is per invocation. Root must be absolute. OutputDir is optional and is
// used only to reject unsafe CLI destinations; Run never writes into it.
type Options struct {
	Root      string
	OutputDir string
}

type VariantName string

const (
	Baseline  VariantName = "baseline"
	Candidate VariantName = "candidate"
)

type CheckKind string

const (
	CheckBuild CheckKind = "build"
	CheckTest  CheckKind = "test"
)

type CheckStatus string

const (
	CheckMatched  CheckStatus = "matched"
	CheckMismatch CheckStatus = "mismatch"
	CheckNotRun   CheckStatus = "not_run"
	CheckError    CheckStatus = "error"
)

type Outcome string

const (
	OutcomeMatched   Outcome = "matched"
	OutcomeMismatch  Outcome = "mismatch"
	OutcomeError     Outcome = "error"
	OutcomeCancelled Outcome = "cancelled"
)

type ErrorKind string

const (
	ErrorInvalidInput   ErrorKind = "invalid_input"
	ErrorInfrastructure ErrorKind = "infrastructure"
	ErrorCancelled      ErrorKind = "cancelled"
)

type FailureKind string

const (
	FailureInvalidInput FailureKind = "invalid_input"
	FailureMissingTool  FailureKind = "missing_tool"
	FailureBuild        FailureKind = "build_failure"
	FailureLaunch       FailureKind = "launch_failure"
	FailureSignal       FailureKind = "signal"
	FailureTimeout      FailureKind = "timeout"
	FailureCancelled    FailureKind = "cancelled"
	FailureOutputLimit  FailureKind = "output_limit"
	FailureIdentity     FailureKind = "identity_change"
	FailureArtifact     FailureKind = "invalid_artifact"
	FailureIO           FailureKind = "io"
	FailureNotRun       FailureKind = "not_run"
)

// Failure contains safe actionable diagnostics, never environment values.
type Failure struct {
	Kind    FailureKind `json:"kind"`
	Message string      `json:"message"`
}

// RunError accompanies a partial report. A behavior mismatch has no Go error.
// Cause supports errors.Is/errors.As and is never serialized.
type RunError struct {
	Kind    ErrorKind `json:"kind"`
	Failure Failure   `json:"failure"`
	Cause   error     `json:"-"`
}

// Error returns "CATEGORY: KIND: MESSAGE", omitting the failure kind when it
// repeats the category: "invalid_input: missing_tool: QEMU executable is
// unavailable", "infrastructure: timeout: ..." or "cancelled: run cancelled by
// caller".
func (e *RunError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Failure.Kind == "" || string(e.Failure.Kind) == string(e.Kind) {
		return string(e.Kind) + ": " + e.Failure.Message
	}
	return string(e.Kind) + ": " + string(e.Failure.Kind) + ": " + e.Failure.Message
}

func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// FileIdentity covers exactly one declared file relative to Options.Root.
type FileIdentity struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type ManifestIdentity struct {
	SHA256 string `json:"sha256"`
	// Encoding is raw_json for unchanged Load bytes, canonical_json otherwise.
	Encoding string `json:"encoding"`
}

type ELFIdentity struct {
	Class   string `json:"class"`
	Machine string `json:"machine"`
	Endian  string `json:"endian"`
	ABI     string `json:"abi"`
}

type ArtifactIdentity struct {
	FileIdentity
	ELF ELFIdentity `json:"elf"`
}

// ToolIdentity records an observation, not environment attestation. VersionError
// explains unavailable version information; tool invocation cannot be assumed to
// support a universal version flag. Version and VersionError are exclusive.
type ToolIdentity struct {
	Role         string `json:"role"`
	Path         string `json:"path"`
	Version      string `json:"version,omitempty"`
	VersionError string `json:"version_error,omitempty"`
}

type TargetInfo struct {
	Kind       TargetKind `json:"kind"`
	HostOS     string     `json:"host_os"`
	HostArch   string     `json:"host_arch"`
	Executable string     `json:"executable,omitempty"`
	Sysroot    string     `json:"sysroot,omitempty"`
}

// Capture keeps raw bytes only in memory. The CLI persists Data into Log using
// restricted permissions. JSON contains metadata and a safe relative log path.
// Bytes counts observed bytes; Data retains at most MaxCaptureBytes.
type Capture struct {
	Data      []byte `json:"-"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Log       string `json:"log,omitempty"`
}

// ProcessObservation separates normal exits (including 127) from launch errors.
// Started=false means no program ran. A normal launched exit sets ExitCode and
// leaves Signal/Failure empty. A signal sets Signal and FailureSignal, never a
// fabricated normal exit. Timeout/cancellation/output limit retain their cause.
type ProcessObservation struct {
	Started        bool     `json:"started"`
	Argv           []string `json:"argv"`
	CWD            string   `json:"cwd"`
	EnvNames       []string `json:"env_names"`
	ExitCode       *int     `json:"exit_code,omitempty"`
	Signal         string   `json:"signal,omitempty"`
	Failure        *Failure `json:"failure,omitempty"`
	Stdout         Capture  `json:"stdout"`
	Stderr         Capture  `json:"stderr"`
	DurationMillis int64    `json:"duration_millis"`
}

// Check records planned values even if Observation is nil and Status=not_run.
type Check struct {
	ID             string              `json:"id"`
	Index          int                 `json:"index"`
	Kind           CheckKind           `json:"kind"`
	Argv           []string            `json:"argv"`
	CWD            string              `json:"cwd"`
	EnvNames       []string            `json:"env_names"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	Expectation    Expectation         `json:"expectation"`
	Observation    *ProcessObservation `json:"observation,omitempty"`
	Status         CheckStatus         `json:"status"`
	Reason         string              `json:"reason"`
}

type VariantReport struct {
	Name            VariantName        `json:"name"`
	CWD             string             `json:"cwd"`
	Build           []Check            `json:"build"`
	Tests           []Check            `json:"tests"`
	ArtifactsBefore []ArtifactIdentity `json:"artifacts_before"`
	ArtifactsAfter  []ArtifactIdentity `json:"artifacts_after"`
}

type ToolVersion struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

// VariantCoverage records the validated declaration before any execution.
// It must never be inferred from observed checks. TestIDs retain the same shared
// manifest order in both variants; BuildIDs are build-0, build-1, and so on.
type VariantCoverage struct {
	Name     VariantName `json:"name"`
	BuildIDs []string    `json:"build_ids"`
	TestIDs  []string    `json:"test_ids"`
}

// Report is authoritative. Zero values and empty evidence are not success.
// Cancellation outranks infrastructure, which outranks mismatch, then matched.
// Invalid-input reports may lack manifest/target identities; they must say error.
type Report struct {
	SchemaVersion int               `json:"schema_version"`
	Tool          ToolVersion       `json:"tool"`
	Target        TargetInfo        `json:"target"`
	Manifest      *ManifestIdentity `json:"manifest,omitempty"`
	Inputs        []FileIdentity    `json:"inputs"`
	Tools         []ToolIdentity    `json:"tools"`
	Declared      []VariantCoverage `json:"declared"`
	Variants      []VariantReport   `json:"variants"`
	Outcome       Outcome           `json:"outcome"`
	Failure       *Failure          `json:"failure,omitempty"`
}

// Plan is per-run data. Root/absolute paths/env values are execution-only and
// must not be serialized. Its ordered slices are baseline then candidate,
// manifest command/test order, and lexically sorted identity paths.
type Plan struct {
	Spec     Spec
	Root     string
	Target   TargetInfo
	Manifest ManifestIdentity
	Inputs   []FileIdentity
	Tools    []ToolIdentity
	Variants []VariantPlan
}

type VariantPlan struct {
	Name        VariantName
	CWD         string
	AbsoluteCWD string
	Build       []CommandRequest
	Tests       []CommandRequest
	Artifacts   []string
}

// CommandRequest contains host build argv or test argv whose first element is
// the validated artifact relative to its variant cwd (with ./ when needed).
// ExecutablePath resolves the executable for launch only, without inserting an
// absolute workspace prefix into argv. Execute wraps test argv with the target
// QEMU/-L prefix when applicable. The executor records actual argv verbatim and
// RelativeCWD instead of AbsoluteCWD. Env overrides only this child's values.
type CommandRequest struct {
	Variant        VariantName
	Kind           CheckKind
	ID             string
	Target         TargetInfo
	Argv           []string
	ExecutablePath string `json:"-"`
	AbsoluteCWD    string
	RelativeCWD    string
	Env            map[string]string `json:"-"`
	TimeoutSeconds int
}

// Component interfaces permit no-call spies at the orchestration boundary.
// Implementations live in separately owned packages; no implementations are
// supplied here. Load/Prepare/InspectArtifacts never launch commands.
type Planner interface {
	Load(io.Reader) (Spec, error)
	Prepare(Spec, Options) (Plan, error)
	InspectArtifacts(Plan, VariantPlan) ([]ArtifactIdentity, error)
}

type Executor interface {
	Execute(context.Context, CommandRequest) ProcessObservation
}

type Evaluator interface {
	Evaluate(Report) Report
	ExitCode(Report) int
}

type Renderer interface {
	WriteJSON(io.Writer, Report) error
	WriteMarkdown(io.Writer, Report) error
}
