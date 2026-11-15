# Quickstart: one baseline, one candidate, native and ARM32

This quickstart lets you try TargetCheck on a tiny C component. You run the
same two tests against a defective build and a fixed build, first natively and
then on ARM32 under QEMU user mode.

The component is synthetic Apache-2.0 example code, not a real
vulnerability. `port-check` contains a `parse_port()` function and runs one
named test case per process. There are two variants of it:

- `baseline/port.c` has a known defect: it accepts `70000` and silently
  truncates it to `4464`.
- `candidate/port.c` is the fix: it rejects values above 65535.

The manifests [native.json](native.json) and [arm.json](arm.json) declare the
same two tests for both variants. They differ only in the target and the
compiler. Each test expects an exact exit code and a marker, which is literal
text that stdout must contain:

| Test | Baseline expectation | Candidate expectation |
| --- | --- | --- |
| `valid-port` | exit 0 and stdout `PASS valid-port` | exit 0 and stdout `PASS valid-port` |
| `reject-out-of-range` | exit 1 and stdout `FAIL reject-out-of-range: "70000" was accepted as 4464` | exit 0 and stdout `PASS reject-out-of-range` |

The baseline's known failure is stated exactly, as an exit code plus a
marker. A crash, a missing program or an unrelated failure cannot match it.

## Prerequisites

- Linux amd64 or arm64.
- `targetcheck` on your `PATH`. From the extracted release directory, run
  `export PATH="$PWD:$PATH"`. Or install it with
  `go install github.com/firmdura/targetcheck/cmd/targetcheck@v1.0.0`.
- For the native run: a C compiler named `cc`.
- For the ARM32 run: `arm-linux-gnueabihf-gcc`, `qemu-arm` and an ARM32
  hard-float sysroot in `/usr/arm-linux-gnueabihf`. In Debian, the packages
  `gcc-arm-linux-gnueabihf`, `libc6-dev-armhf-cross` and `qemu-user` provide
  them.

The pinned development image (`make container-build`) contains all of these
tools. [docs/usage.md](../../docs/usage.md#the-pinned-development-container)
shows how to build `targetcheck` inside it.

Run the commands from the directory that contains `examples/`: the extracted
release directory or a source checkout. Each run first copies this example
into a new scratch directory, for two reasons:

- The build commands write `port-check` into the variant directories.
- TargetCheck refuses to overwrite an existing report directory.

## Native

<!-- quickstart:native -->
```sh
work=$(cd "$(mktemp -d)" && pwd -P)
cp -R examples/quickstart "$work/root"
targetcheck check --manifest "$work/root/native.json" --root "$work/root" --output "$work/report"
cat "$work/report/logs/baseline-test-1.stdout"
```

The command builds both variants with `cc`, runs both tests in each variant
and exits 0:

```text
targetcheck: matched
JSON: "/tmp/tmp.XXXXXXXXXX/report/report.json"
Markdown: "/tmp/tmp.XXXXXXXXXX/report/report.md"
FAIL reject-out-of-range: "70000" was accepted as 4464
```

The last line is the baseline's recorded output for `reject-out-of-range`.
The known defect matched its declared expectation. The report directory
holds:

- `report.json`, the authoritative evidence;
- `report.md`, a readable rendering of the same report;
- `logs/`, the captured stdout and stderr of every build and test step.

## See a mismatch

Now make the candidate defective too, by copying the baseline source over the
fixed candidate source. The candidate then fails `reject-out-of-range`, and
TargetCheck exits 1:

<!-- quickstart:regression -->
```sh
work=$(cd "$(mktemp -d)" && pwd -P)
cp -R examples/quickstart "$work/root"
cp "$work/root/baseline/port.c" "$work/root/candidate/port.c"
targetcheck check --manifest "$work/root/native.json" --root "$work/root" --output "$work/report"
echo "exit $?"
```

```text
targetcheck: mismatch
JSON: "/tmp/tmp.XXXXXXXXXX/report/report.json"
Markdown: "/tmp/tmp.XXXXXXXXXX/report/report.md"
exit 1
```

In `report.json`, the candidate's `reject-out-of-range` check says why it did
not match:

```json
"status": "mismatch",
"reason": "exit 1; expected 0; stdout missing literal marker \"PASS reject-out-of-range\""
```

## ARM32 under QEMU user mode

`arm.json` cross-compiles the same sources with `arm-linux-gnueabihf-gcc`. It
runs each test as `qemu-arm -L /usr/arm-linux-gnueabihf ./port-check CASE`:

<!-- quickstart:arm -->
```sh
work=$(cd "$(mktemp -d)" && pwd -P)
cp -R examples/quickstart "$work/root"
targetcheck check --manifest "$work/root/arm.json" --root "$work/root" --output "$work/report"
grep -m 2 -E '"(class|machine)"' "$work/report/report.json"
```

```text
targetcheck: matched
JSON: "/tmp/tmp.XXXXXXXXXX/report/report.json"
Markdown: "/tmp/tmp.XXXXXXXXXX/report/report.md"
            "class": "ELF32",
            "machine": "EM_ARM",
```

The last two lines come from the recorded identity of the first tested
artifact: a 32-bit ARM executable.

This is a component test of ARM32 binaries under user-mode emulation with a
Debian cross sysroot. It is not a test on ARM hardware or of a firmware image;
see
[docs/limitations.md](../../docs/limitations.md#emulator-component-tests-versus-physical-product-qualification).

## Next steps

- Remove a scratch directory with `rm -rf "$work"` when you no longer need it.
- To compare your own component, see
  [Writing a manifest for your component](../../docs/usage.md#writing-a-manifest-for-your-component)
  in docs/usage.md.
