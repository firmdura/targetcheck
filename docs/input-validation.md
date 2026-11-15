# Input planning and declared identities

`internal/plan` implements the frozen planner interface through the stateless
`Service` and package functions `Load`, `Prepare` and `InspectArtifacts`.
It never starts a program, downloads data, changes the cwd or environment,
writes a workspace file, evaluates expectations or renders a report.

In short, the planner guarantees that:

- `Load` returns a Spec only for one strictly valid manifest;
- `Prepare` checks both variants and every declaration before it returns any
  command request, and a failure exposes no runnable plan;
- declared paths cannot escape Root or pass through symlinks;
- inputs are hashed before builds, and artifacts must be safe executable ELF
  files of the selected target.

## Strict manifests

`Load` accepts one UTF-8 JSON value of at most 1 MiB. It rejects malformed or
trailing JSON, duplicate keys at every object depth (including escaped aliases),
unknown/case-alias keys, null required fields and invalid field types. Required
members, exact version, target configuration, declarations, unique test IDs,
timeouts, argv and environment names are validated before a Spec is returned.
The schema shape is [manifest v1](../schema/manifest-v1.json).

Native manifests cannot contain QEMU-only fields. QEMU requires an executable
and sysroot. Candidate expectations require normal exit zero; a nonzero baseline
expectation requires at least one nonempty literal marker. Every supplied marker
must be nonempty. Later empty argv arguments are allowed; the executable is not.
No argv/environment value may contain NUL. There is no variable expansion.

Both variants declare at least one input and artifact. Tests share one ordered
argv list, whose executable must name a declared artifact in each variant. Paths
are normalized lexically, and normalized duplicate declarations are rejected.
Absolute declared paths and every `..` component are rejected. Each variant cwd
must be below Root, so Root itself (`.`) is rejected, and the two normalized
directories must not overlap: they must differ and neither may contain the other.
A build may write anywhere below its own cwd, so an overlapping layout would let
one variant's build rewrite the other variant's declared inputs after they were
hashed. Overlap is invalid input, reported before any command runs, for example
`baseline cwd "a" and candidate cwd "a/b" overlap; variant directories must
differ and neither may contain the other`. Directories are compared by whole
path components after normalization, so siblings that only share a name prefix,
such as `a` and `ab`, are allowed.

## Preflight without execution

`Prepare` requires Linux amd64/arm64 and an explicit absolute Root. It validates
both variants and all declarations before returning any command requests. Every
failure returns a zero Plan and a typed invalid-input error; unavailable explicit
tools use the `missing_tool` failure kind. No partial runnable plan is exposed.

Declared inputs must be ordinary readable files. Root, variant directories and
all declared file ancestors are opened using directory file descriptors and
`O_NOFOLLOW` at every component. Symlinks, path-type substitutions and unreadable
files are rejected. Nonblocking opens avoid hanging on a substituted FIFO before
its type is checked. This path policy deliberately rejects symlinked Root paths
and ancestors too; supply their actual absolute path.

Build tools must exist and be executable before any variant starts. PATH lookup
uses that command's explicit PATH override, or inherited PATH, with relative PATH
entries interpreted under the child cwd. Tool symlinks are resolved to their
actual paths; system tools are not restricted to Root. The request launches that
resolved absolute path (`ExecutablePath`, excluded from JSON) with the original
argv. Environment overrides are copied into each request without changing the
caller environment.

The tool observation records the same location relative to Root, with forward
slashes, when it lies inside Root, like report cwd, input and artifact paths.
Otherwise it records the absolute host path. Containment is decided after
symlink resolution:

| Tool | Reported path |
| --- | --- |
| `./configure` in cwd `baseline` | `baseline/configure` |
| PATH-resolved `make`, unless Root contains it | `/usr/bin/make` |
| PATH-resolved `make` with Root `/`, where every tool is inside Root | `usr/bin/make` |
| A workspace link to a system tool | the absolute system path |
| A link outside Root that resolves into Root | the Root-relative location |

Two cases tie the reported path to Root's location:

- A tool outside Root that is reached through a relative path from inside it,
  such as `../` argv, a relative PATH entry or a relative symlink, reports an
  absolute path. That path can change when Root moves, because the relative
  path can then reach another file.
- Conversely, a reference resolved from outside Root that lands inside it, such
  as an absolute argv, PATH entry or symlink target naming a location in Root,
  is reported Root-relative only under that Root. With the same manifest and a
  copy of the workspace at another Root, it still reaches the original file,
  which then runs and is reported by its absolute path.

Only tool paths and an explicit unavailable-version observation are collected
here: this package does not launch version probes or claim the tool's transitive
runtime dependencies are attested. Build tools are never run speculatively, and
their observation says so and suggests an explicit build step such as
`["gcc", "--version"]` instead. The QEMU observation is completed after
preflight by orchestration's single version query; see
[Command execution](execution.md#qemu-version-query).

The QEMU executable must be available and executable, and the supplied sysroot
must be a readable directory. A relative QEMU executable with `/` and a relative
sysroot resolve against Root; a bare executable is resolved through inherited
PATH.

Both are recorded in `target` and in the `qemu` and `sysroot` tool
observations as their symlink-resolved absolute paths, even inside Root. The
executor passes exactly these strings as the QEMU program and `-L` argument and
records that argv verbatim, and evaluation requires the observed prefix to
equal `target`. A QEMU executable or sysroot inside Root therefore ties those
report fields and the observed QEMU test argv to Root's location whenever a run
under another Root uses that Root's own copy. One named by an absolute path,
PATH entry or symlink target keeps resolving to the original location instead,
so every run uses the original and the fields stay the same. Keep both outside
Root, and do not reach them through paths relative to Root, when reports from
different roots must compare equal.

Static ARM artifacts can use an otherwise empty sysroot. For dynamic ARM
artifacts, inspection additionally requires the ELF interpreter to exist as an
executable ordinary file inside that sysroot. Full shared-library and
environment qualification remains outside declared-file identity.

The sysroot also receives a named tool observation with its resolved path and a
truthful unavailable-version reason: directories have no universal version
query, and this preflight does not attest the directory's complete contents.

Existing artifacts must already be safe executable ELF files of the selected
target. Artifacts may be absent when build steps are declared, including absent
parent directories; existing ancestors must still be safe. With no build steps,
artifacts must be present during preflight. Every artifact is inspected again
after its build, regardless of preflight observations.

Optional `Options.OutputDir` must be an absolute, new destination outside both
variant directories. Its parent must exist and have no symlink ancestors. This
is validation only: the CLI separately owns exclusive creation and write checks.

## Stable identities and requests

Inputs are hashed before builds, one file per declared path. Variant source/config
paths and shared test-input paths are deduplicated across declaration categories
and emitted in sorted Root-relative order, with byte sizes and SHA-256. File
size/mtime changes during hashing fail rather than producing a claimed stable
identity. These hashes cover declared files only and do not authenticate them.

Unchanged Specs loaded by `Load` retain an exact raw-JSON digest. Before using
the original bytes, `Prepare` strictly decodes and compares them with the current
Spec. A changed/manual Spec instead receives a labelled canonical-JSON digest;
nil empty build/shared-input lists are represented as arrays. Returned Spec,
request argv, environment maps and original bytes do not alias caller state.

Requests retain relative variant cwd and original build argv. Test executables
are normalized to `./<artifact>` relative to their variant, with absolute launch
paths kept separately in `ExecutablePath`. QEMU wrapping belongs to the executor.
Absolute workspace prefixes are not injected into observed argv.

`InspectArtifacts` re-opens each original artifact declaration without symlinks,
then inspects ELF and hashes the same file descriptor. It returns sorted identities
or a typed infrastructure/`invalid_artifact` error, never an empty successful
identity. Supported ELF is little-endian Linux amd64/arm64 natively, or ARM32
EABI5 hard-float for QEMU. Inspection requires an executable/PIE ELF type and
nonzero entry, and rejects wrong class/machine, unsupported OS ABI and invalid
ARM float/EABI flags. Orchestration
compares the before/after sets to classify an identity change.

Trusted build programs can alter workspace files. Revalidation detects the
declared post-build/test state; this tool is not a sandbox against malicious
programs or a full filesystem/environment attestation mechanism.

## Verification

The focused command is `go test -race ./internal/plan/...` in the pinned Linux
environment, with networking disabled after image preparation. Tests cover
strict decoding and limits, both-variant preflight failures, tool/PATH handling,
symlink and type replacement, unreadable/missing files, output destinations,
repeatable identities, caller-state independence and artifact target checks.
Native positive artifact inspection uses the actual compiled Go test binary.
Minimal ARM headers exercise structural validation only; they are never executed
or represented as ARM/QEMU acceptance evidence.

Failed preflight tests assert that no runnable requests escape. The orchestration
tests in `internal/orchestrate` attach an executor spy to the production
orchestration path and check that preflight errors cause zero launches there;
planner tests alone do not establish that integration behavior.
