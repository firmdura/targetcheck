# Walkthrough: the real GNU Wget CVE-2026-58470 case

CVE-2026-58470 is an integer overflow in `parse_content_range()`, the GNU Wget
function that parses the HTTP `Content-Range` header. This walkthrough runs
TargetCheck on the real Wget 1.21.4 code, natively on a 64-bit host and as
32-bit ARM (ARM32) binaries under QEMU user mode. You will see three results:

1. **The original bug on a 64-bit host.** The unpatched parser overflows a
   signed 64-bit integer. The tests are built with UndefinedBehaviorSanitizer
   (UBSan) in no-recovery mode: the compiler adds checks that stop the
   program at undefined behavior, such as this overflow, and report the
   source line.
2. **A first fix that breaks large offsets on 32-bit ARM.** The first
   upstream fix alone passes the four large-offset cases on the 64-bit host.
   On ARM32, where `long` is 32 bits, the same fix rejects those offsets.
3. **A complete fix that passes both.** The complete fix passes the same
   boundary and compatibility tests natively and on ARM32.

The second result is the reason to run the tests on the target architecture:
the first fix passes the large-offset cases natively, so only the ARM32 run
shows this regression.
Each failure is identified by a named diagnostic in the logs, such as the
UBSan report or a named test assertion. The evidence covers the named test
cases only; [What the evidence does not show](#what-the-evidence-does-not-show)
lists the limits.

## The three variants

The demo builds Wget's own unit-test program, `tests/unit-tests`, from the
pinned source tarball <https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz>. A
variant is one patched copy of that source:

| Variant | Fix patches |
| --- | --- |
| `unpatched` | none |
| `first-fix` | only upstream [43d3ba9](https://gitlab.com/gnuwget/wget/-/commit/43d3ba9336bc94937e6fae2365c6ffd30c34ffcf), the fix named by the [Ubuntu advisory](https://ubuntu.com/security/CVE-2026-58470) |
| `complete` | 43d3ba9, the upstream follow-up [01ff771](https://gitlab.com/gnuwget/wget/-/commit/01ff771caac1958662ca8665eed2021ec386a7af) (`strtoll` instead of `strtol`) and a local range-length guard |

Every variant also gets the same test-only patch, which adds the named test
cases. The recipe, provenance and per-file licenses are in
[examples/wget/README.md](../examples/wget/README.md).

The demo runs seven comparisons, called rows, one per committed manifest. In
five rows, TargetCheck builds and tests a baseline variant and a candidate
variant and checks each against its declared expectations. The other two rows
check that a missing emulator or sysroot is reported at preflight, the checks
TargetCheck makes before it runs any command.

## Before you start

You need, on the host:

- Docker. On macOS, use a Linux VM such as Colima: TargetCheck runs only on
  Linux amd64 and arm64.
- Git 2.30 or later, for `git archive --add-file`.
- POSIX `sh`, `curl`, and one of `sha256sum`, `shasum` or `openssl`.
- Network access, but only for the two preparation commands below.

Everything else comes from the digest-pinned image described in
[dev/tool-versions.txt](../dev/tool-versions.txt): Go 1.27.4, GCC 12.2,
`arm-linux-gnueabihf-gcc` 12.2 with the armhf sysroot and UBSan runtime,
`qemu-arm` 7.2.22, GNU patch 2.7.6 and make 4.3.

## Run the demo

From a macOS or Linux Docker host, at the top of the checkout:

```sh
make container-build   # network: build the pinned image once
make prepare-demo      # network: fetch the tarball and verify its SHA-256
make demo-container    # offline: verify, test-native, test-arm and demo
```

1. `make container-build` builds the pinned image. You need to run it once.
2. `make prepare-demo` downloads
   <https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz> into the ignored
   `.cache/wget/` directory. The file must have SHA-256
   `81542f5cefb8faacc39bbbc6c82ded80e3e4a88505ae72ea51df27525bcde04c`. A
   cached tarball with another hash is refused with a nonzero exit, and it is
   neither extracted nor replaced. Delete it and run the command again.
3. `make demo-container` re-verifies the cache. It sends `git archive HEAD`
   and the verified tarball into the pinned image, and nothing else from your
   working tree. There it runs `make verify test-native test-arm demo` with
   `--network=none`. Uncommitted edits are therefore not tested.

The complete log and the demo's reports, logs and summaries come back even
when a target fails. They land in a new directory,
`.cache/test-container/COMMIT.XXXXXX/`, which the command names at the end:

```sh
ls .cache/test-container/*/wget-demo/demo.*/
```

[Reading the evidence](#reading-the-evidence) explains what you find there.

### Other ways to run it

`make test-container` runs the same suite plus `make dist`. To run only part
of the suite, override the targets, for example
`make demo-container DEMO_CONTAINER_TARGETS='test-arm demo'`.

On a Linux host that already has the pinned toolchain, or inside the image
with the checkout bind-mounted, these targets run directly after
`make prepare-demo`:

```sh
make verify test-native test-arm demo
```

To open a shell in the image with the checkout bind-mounted, run
`docker run --rm -it --network=none -v "$PWD:/src" -w /src targetcheck-dev:go1.27.4 bash`.
On macOS, the checkout must be in a directory shared with the Docker VM.

### Which TargetCheck commit the reports name

The Make targets build with Go's VCS stamping disabled. They therefore also
work when git refuses a bind-mounted checkout owned by another user
("detected dubious ownership"). Without stamping, `make demo` records the
TargetCheck commit only from `TARGETCHECK_COMMIT` or from a clean checkout
that git can read. Otherwise its reports say `commit=unknown`, and
`environment.txt` states which case applied. `make demo-container` and
`make test-container` always record the archived commit, and they test a
clean snapshot instead of your checkout.

### Missing tools and old evidence

`make test-arm` and `make demo` fail with an actionable message when
`qemu-arm`, the cross compiler or the sysroot is missing. They never skip.

Each `make demo` keeps its evidence in a new `.cache/wget-demo/demo.*`
directory. Remove old ones with `rm -rf .cache/wget-demo`.

## What `make demo` does

[scripts/demo.sh](../scripts/demo.sh) runs offline, in five steps:

1. It checks the tools and re-verifies `.cache/wget/wget-1.21.4.tar.gz` with
   `scripts/prepare-demo.sh --verify-only`.
2. It corrupts one byte of a private copy of the tarball. Both
   `prepare-demo.sh --verify-only` and `make-variant.sh` must refuse that
   copy, with nothing extracted.
3. It builds the CLI and the checker,
   [examples/wget/democheck](../examples/wget/democheck/main.go). It records
   tool versions, resolved paths and binary SHA-256 values in
   `environment.txt`.
4. It runs the whole matrix twice, in new directories `run-1` and `run-2`.
   Each run first creates every source variant with
   [make-variant.sh](../examples/wget/make-variant.sh), from a private,
   re-verified copy of the tarball. It then runs this command for each of the
   seven committed manifests, where ROW is the manifest name and RUN is the
   run directory:

   ```sh
   targetcheck check --manifest examples/wget/manifests/ROW.json \
     --root RUN/root --output RUN/reports/ROW
   ```

   Each command runs in a minimal environment
   (`env -i PATH=... LC_ALL=C TZ=UTC`). TargetCheck builds both variants with
   the recorded `configure` and `make` argv, then runs the tests, under
   `qemu-arm -L /usr/arm-linux-gnueabihf` for ARM rows. The checker then
   asserts each row.

   After run 1, the demo also tests the checker. It renames the ARM
   candidate's named assertion in a copy of the evidence, keeping the same
   byte length, and requires the checker to reject that copy. A different
   exit-1 failure therefore cannot pass as the intended one.
5. It requires the normalized results of the two runs to be byte-identical.

### What the checker asserts

For every row, the checker requires:

- the exact CLI exit and the report outcome;
- the raw manifest digest and the declared coverage;
- every build step's argv and exit;
- each named test's status, exit code and observed argv, including the
  `qemu-arm -L SYSROOT` prefix;
- the expectation markers and the named diagnostic lines in the persisted
  logs;
- no UBSan report in any candidate output;
- the `TYPE SIZES` line that each test prints: a 32-bit `long` on ARM and a
  64-bit one natively;
- unchanged artifact identities, that is the tested binary's SHA-256, size
  and ELF fields recorded before and after testing, with the expected ELF
  class, machine and ABI;
- declared input digests that match the reviewed variant sources, with
  identical instrumentation in both variants.

The same variant built for the same target must produce the same artifact
digest in every row and in both runs. The two preflight-failure rows must
report no variants or coverage, because no command ran. Their manifests must
differ from `b-arm-large-offset-first-fix.json` only in the target executable
or sysroot.

## Expected results

| Row | Target | Baseline -> candidate | CLI exit | Key evidence |
| --- | --- | --- | --- | --- |
| `a-native-security` | native | unpatched -> complete | 0 | baseline UBSan overflow in all three fields; complete passes 10/10 |
| `b-native-large-offset-first-fix` | native | unpatched -> first-fix | 0 | first fix passes the 4 large-offset cases on 64-bit |
| `b-arm-large-offset-first-fix` | ARM32 QEMU | unpatched -> first-fix | 1 | first fix fails 4/4 with `range_accepted` |
| `b-arm-large-offset-corrected` | ARM32 QEMU | first-fix -> complete | 0 | the first-fix failure is matched as the expected baseline; complete passes |
| `c-arm-security` | ARM32 QEMU | unpatched -> complete | 0 | same UBSan diagnostics on ARM32; complete passes 10/10 |
| `missing-qemu` | ARM32 QEMU | preflight | 2 | `missing_tool`: QEMU executable is unavailable |
| `missing-sysroot` | ARM32 QEMU | preflight | 2 | `invalid_input`: QEMU sysroot is unavailable |

A failing candidate exits 1 and a missing emulator or sysroot exits 2, so the
two cannot be confused. In the last two rows TargetCheck stops at preflight,
so nothing is built.

Two of the diagnostics to look for are the UBSan report of the native
unpatched baseline and the first fix failing a large-offset case on ARM32:

```text
http.c:936:9: runtime error: signed integer overflow: 9223372036854775800 + 8 cannot be represented in type 'long int'
content-range assertion range_accepted failed: case=large-offset-first-byte-pos subcase=above-2gib header="bytes 2147483648-2147483649/2147483650": parser rejected a valid header; expected first=2147483648 last=2147483649 length=2147483650
```

## Reading the evidence

The demo prints its evidence directory, for example
`.cache/wget-demo/demo.XXXXXX/`. With `make demo-container` that directory is
inside the container, and it comes back as
`.cache/test-container/COMMIT.XXXXXX/wget-demo/demo.XXXXXX/`. Start with
`run-1/summary.md`; each row's `report.json` holds the full record.

| Path | Content |
| --- | --- |
| `environment.txt` | tool versions, resolved paths and SHA-256 values for GCC, the ARM cross compiler, `qemu-arm`, patch, make and the native and sysroot runtimes |
| `corrupted-cache/*.log` | the refusals of the corrupted tarball copy |
| `run-N/summary.md` | per-row exits, named test outcomes, diagnostic lines, artifact SHA-256 values and ELF identities |
| `run-N/normalized.json` | the compared result: coverage, statuses, exit codes, test-output digests, input and artifact identities |
| `run-N/reports/ROW/` | TargetCheck `report.json` (authoritative), `report.md` and per-step logs |
| `run-N/variants/` | each variant's `TARGETCHECK-VARIANT.txt`: patch hashes and patch output (01ff771 applies at a -14 line offset, no fuzz) |
| `run-N/cli/` | CLI stdout, stderr and exit code per row |
| `tampered-run-1/democheck.log` | the checker's rejection of the tampered diagnostic |
| `run-N/root/` | the variant trees with their build outputs and tested binaries (about 330 MB per run; not copied back by `make demo-container`) |

In each `report.json`:

- the build argv shows the exact `configure` flags;
- `inputs` lists the SHA-256 of each variant's `TARGETCHECK-SOURCE.sha256`
  (a digest of every source file), `src/http.c` and the instrumentation
  files;
- `artifacts_before` and `artifacts_after` record the SHA-256 and the ELF
  class, machine and ABI of `tests/unit-tests`, for example
  `ELF32 EM_ARM little arm-eabi5-hard-float`. That is a 32-bit little-endian
  ARM binary for ARM EABI version 5 with the hardware floating-point calling
  convention.

## What the evidence does not show

The evidence establishes one thing: for the identified binaries, Wget's
production `parse_content_range()` meets the declared expectations of the
named cases in [examples/wget](../examples/wget/README.md). It does **not**
establish:

- complete Wget security, or that every path to the overflow is repaired;
- a complete repair of caller-side overflow, as described below;
- that any product, customer device or firmware image is exposed or fixed;
- firmware qualification, a vendor feature configuration or physical-device
  validation.

The build uses the smallest configuration sufficient for these component
tests, not a vendor firmware configuration; see
[Build configuration](../examples/wget/README.md#build-configuration). The
ARM32 runs are component tests under `qemu-arm` user mode with the Debian
cross sysroot, not runs on a target board. The hashes identify the files that
were tested. They do not prove trusted provenance of the toolchain or the
host.

### The local guard and its residual

The local range-length guard (`patches/0003-...`) is a local addition pending
upstream review. It is not upstream-approved. The guard and the
`caller_length` assertion cover only `last_byte_pos - first_byte_pos + 1`
with a zero first position.

One caller-side overflow remains at the maximum endpoint. It was found by
reading the pristine 1.21.4 `src/http.c` only, and no test here exercises it:

- `gethttp()` also computes `hs->contlen = contlen + contrange`, which equals
  `last_byte_pos + 1`.
- Take `bytes R-9223372036854775807/*`, where `R` is a nonzero resume offset
  (`--start-pos=R` or `-c`). Wget's range check accepts that response.
- The guard does not apply when `R > 0`, so the sum still overflows.

The [recipe](../examples/wget/README.md#residual-not-covered-by-the-guard)
gives the source lines.

## Licensing

TargetCheck is Apache-2.0: the Go library, the CLI, the scripts and this
document. Everything derived from GNU Wget lives in `examples/wget/` and is
GPL-3.0-or-later with the upstream notices: the unchanged `COPYING`, the two
upstream patches and the two local patches. Wget code is compiled only into
the Wget test program, inside a scratch workspace. It is never linked into
TargetCheck. See [examples/wget/NOTICE](../examples/wget/NOTICE).

## Run one comparison by hand

To run one row yourself, open a shell in the pinned image with the checkout
bind-mounted, as in [Other ways to run it](#other-ways-to-run-it), after
`make prepare-demo`. These commands create the `unpatched` and `complete`
variants and compare them on ARM32, like row `c-arm-security`:

```sh
root=$(mktemp -d) && out=$(mktemp -d) && mkdir "$root/c-arm-security"
for v in unpatched complete; do
  sh examples/wget/make-variant.sh "$v" .cache/wget/wget-1.21.4.tar.gz "$root/c-arm-security/$v"
done
go build -buildvcs=false -o "$out/targetcheck" ./cmd/targetcheck
"$out/targetcheck" check --manifest examples/wget/manifests/c-arm-security.json \
  --root "$root" --output "$out/report"
echo "exit $?"
grep -h 'runtime error' "$out"/report/logs/baseline-test-*.stderr
```

The comparison prints `targetcheck: matched` and the report paths, and exits
0. The baseline stderr logs contain the `'long long int'` UBSan lines for the
three oversized fields.

`-buildvcs=false` keeps the build working when git does not trust the mounted
checkout, so this report records the TargetCheck commit as `unknown`. Build
and test commands are trusted programs: the manifest's `configure` and `make`
run with your permissions inside `$root`.

## Check that a corrupted tarball is refused

The demo checks this automatically. To see it by hand on any host, corrupt a
copy of the tarball. Byte 1000000 of the pinned tarball is not `X`, so writing
`X` there changes the file:

```sh
mkdir -p .cache/corrupt && cp .cache/wget/wget-1.21.4.tar.gz .cache/corrupt/
printf X | dd of=.cache/corrupt/wget-1.21.4.tar.gz bs=1 seek=1000000 conv=notrunc
WGET_CACHE_DIR=.cache/corrupt sh scripts/prepare-demo.sh; echo "exit $?"
```

The script prints
`prepare-demo: refusing ...: SHA-256 ... does not match the pinned ...` and
exits 1. It does not extract, replace or use the file.

A cached `.cache/wget/wget-1.21.4.tar.gz` in that state stops
`make prepare-demo`, `make demo` and `make demo-container` the same way.
Delete it and run `make prepare-demo` again.

## Troubleshooting

| Message | Cause and fix |
| --- | --- |
| `Use the pinned Linux container` | `make demo`, `make test-arm` and `make verify` need Linux and Go 1.27.4. Use `make demo-container`. |
| `missing required tools or ARM sysroot files` or `test-arm: FAIL: missing` | Install `qemu-user`, `gcc-arm-linux-gnueabihf` and `libc6-dev-armhf-cross` (with `libubsan1-armhf-cross`), or use the image. |
| `detected dubious ownership` or `error obtaining VCS status`, from your own `go build` in a bind-mounted checkout | Git does not trust a checkout owned by another user. Add `-buildvcs=false`; the binary then reports commit `unknown`. Or run `git config --global --add safe.directory /src` inside the throwaway container. |
| `SHA256SUMS has no entry` or `SHA-256 mismatch for patches/...` | A patch or series file was changed. The recipe only creates reviewed variants. |
| A row fails in `democheck` | The message names the row, variant and test, and the missing diagnostic or identity. Compare it with `run-N/reports/ROW/`. |
