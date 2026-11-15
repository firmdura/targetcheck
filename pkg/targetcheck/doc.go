// SPDX-License-Identifier: Apache-2.0

// Package targetcheck runs the same component regression tests against two
// builds of a component and reports whether each test met its declared
// expectation for the identified binaries.
//
// The two builds are called variants. A typical use is checking a fix: the
// baseline variant is the code before the fix, and the candidate variant is
// the code with it. Tests run on Linux amd64 and arm64
// hosts, either natively or under an explicitly configured qemu-arm
// user-mode emulator and sysroot. Under qemu-arm, the tested binaries must be
// little-endian ARM32 EABI5 hard-float, the ABI that Debian calls armhf. A
// matched report is evidence about the declared tests only. It is not a
// security-fix certification.
//
// # Entry points
//
//   - [Load] strictly decodes one schema-version-1 JSON manifest into a
//     [Spec]. It starts no programs.
//   - [Run] first checks both variants, their declared inputs and the tools
//     (preflight). Then it builds and tests the baseline, then the candidate,
//     and returns the evaluated [Report]. Options.Root must be an absolute
//     workspace directory.
//   - [WriteJSON] writes the authoritative JSON report.
//   - [WriteMarkdown] writes the same evidence as Markdown, with untrusted
//     text escaped.
//
// # Errors and partial reports
//
// A test that does not meet its expectation is a mismatch in the report, not
// a Go error. Run returns a partial report together with a *[RunError] in
// three cases: invalid input, an infrastructure failure (for example a failed
// build, a timeout, an output limit or a changed artifact identity), and
// cancellation.
//
// Use [errors.As] to get the RunError. Its Kind is the category:
// [ErrorInvalidInput], [ErrorInfrastructure] or [ErrorCancelled]. Its Failure
// holds the specific [FailureKind] and the message that the report also
// records. [errors.Is] reaches the cause, such as [context.Canceled]. A
// partial report keeps every completed check. Checks that did not run are
// [CheckNotRun], never passed.
//
// # Trusted commands
//
// Build and test commands are trusted, arbitrary programs. Run executes them
// directly, without a shell, with the caller's permissions and environment.
// It is not a sandbox.
//
// Besides those commands, Run starts one process, only for a qemu-arm target
// and only after preflight succeeded: the configured emulator with the single
// argument --version. The report records the first non-empty line that it
// prints as the emulator's version.
//
// Run never writes reports, downloads files, installs tools, calls os.Exit or
// changes the caller's working directory or environment. The targetcheck
// command, github.com/firmdura/targetcheck/cmd/targetcheck, runs the same
// engine and writes the reports and per-step logs to a new directory.
//
// # Types and further documentation
//
// The exported types are aliases of internal types, so this page does not
// list their fields. The library guide, docs/library.md in
// https://github.com/firmdura/targetcheck, lists them with their JSON names.
// The same docs directory describes the manifest and report formats, the
// validation rules, outcomes and limits: docs/usage.md is the user guide, and
// docs/contracts.md is the full contract.
package targetcheck
