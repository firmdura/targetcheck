# Real case: GNU Wget 1.21.4, CVE-2026-58470

This directory is a self-contained, GPL-labelled recipe. It rebuilds GNU
Wget's own unit-test program from the pinned public 1.21.4 source in three
source variants, and uses TargetCheck to compare them natively and on ARM32
under QEMU user mode. To run it, follow the step-by-step guide in
[docs/example-walkthrough.md](../../docs/example-walkthrough.md).

The claim is narrow. For the identified binaries, the production
`parse_content_range()` function in `src/http.c` meets the declared
expectations of the named cases below. It is not:

- a complete Wget security claim;
- a statement about exposure in any product;
- firmware qualification or device validation;
- a complete repair of caller-side overflow; see the
  [residual the guard does not cover](#residual-not-covered-by-the-guard).

## Licensing

| Files | License |
| --- | --- |
| `COPYING` | GNU GPL version 3 text, unchanged from the wget-1.21.4 tarball |
| `patches/*.patch` | GPL-3.0-or-later (upstream GNU Wget commits and local modifications of Wget) |
| `NOTICE` | Upstream copyright, license and authorship notices for the files above, and the list of local modifications |
| `README.md`, `SHA256SUMS`, `make-variant.sh`, `matrix.json`, `series/*`, `manifests/*.json`, `democheck/main.go` | Original TargetCheck recipe files, Apache-2.0; they contain no Wget code |

TargetCheck itself, the Go library and CLI, is Apache-2.0. None of this
Wget-derived material is ever compiled into or linked with it. The Wget
source tarball is not redistributed; `make prepare-demo` fetches it.

## Provenance

- Source: <https://ftp.gnu.org/gnu/wget/wget-1.21.4.tar.gz>, SHA-256
  `81542f5cefb8faacc39bbbc6c82ded80e3e4a88505ae72ea51df27525bcde04c`.
- Advisory: <https://ubuntu.com/security/CVE-2026-58470> (published 7 July
  2026) states that GNU Wget through 1.25.0 has an integer overflow in
  `parse_content_range()` in `src/http.c`, where server-controlled
  Content-Range values make signed integer arithmetic overflow, and lists
  upstream commit 43d3ba9 as the fix.
- First upstream fix:
  <https://gitlab.com/gnuwget/wget/-/commit/43d3ba9336bc94937e6fae2365c6ffd30c34ffcf>,
  "* src/http.c (parse_content_range): Fix integer overflow", Tim Rühsen,
  2026-06-29. It replaces the unchecked `num = 10 * num + digit` loops with
  `strtol()` and `ERANGE` checks.
- Upstream follow-up:
  <https://gitlab.com/gnuwget/wget/-/commit/01ff771caac1958662ca8665eed2021ec386a7af>,
  "* src/http.c (parse_content_range): Use strtoll instead of strtol.", Tim
  Rühsen, 2026-08-12. `wgint` is `int64_t`, but `long` is 32 bits on ARM32,
  so `strtol()` rejects valid offsets above 2147483647 there.
- Both commits were on upstream `master` when checked on 2026-11-15. The
  committed patch files are byte-identical to the GitLab `.patch` output
  (author, date and subject headers preserved); their SHA-256 values are in
  [SHA256SUMS](SHA256SUMS).
- No backport adaptation is needed. Both upstream patches apply to the
  pinned 1.21.4 source, in series order, with `patch -p1 --fuzz=0`. 01ff771
  applies with a -14 line offset and no fuzz, because upstream `src/http.c`
  had 14 more lines above `parse_content_range()` when the follow-up was written.

## Local additions (not upstream)

Two patches are local additions. They are not upstream GNU Wget changes.

### Test instrumentation

`patches/0000-test-content-range-unit-tests.patch` is test-only
instrumentation. It is applied first, and identically, to every variant.

- It adds `tests/content-range-cases.c`, which `src/http.c` includes inside
  its existing `TESTING` section. Every case therefore calls the production,
  file-static `parse_content_range()`.
- The cases run in Wget's own unit-test program, `tests/unit-tests`, and
  follow its MinUnit convention. Upstream links that program, from
  `tests/unit-tests.c` and `tests/unit-tests.h`, against the `-DTESTING`
  build of the Wget sources.
- With arguments, `unit-tests` runs only the named cases. Without arguments,
  the upstream test list is unchanged.

### Local range-length guard

`patches/0003-local-reject-overflowing-range-length.patch` is a guard
**pending upstream review, not upstream-approved**. After a successful parse,
`gethttp()` computes `last_byte_pos - first_byte_pos + 1`. With an unknown
length (`*`), even the upstream-fixed parser accepts
`bytes 0-9223372036854775807/*`, whose length overflows `wgint`. The guard
rejects exactly a zero first position with a `WGINT_MAX` last position.

### Residual not covered by the guard

One caller-side overflow is not covered. It was found by code reading of the
pristine 1.21.4 `src/http.c` only, and no test here exercises it:

- After the range check at line 4056, `gethttp()` also computes
  `hs->contlen = contlen + contrange` at line 4071. The same sum is printed at
  lines 4083-4086. It equals `last_byte_pos + 1`.
- Take `bytes R-9223372036854775807/*`, with `R` equal to a nonzero resume
  offset (`restval`, from `--start-pos=R` or `-c`). The range check accepts
  that response.
- The guard does not apply because `R > 0`, and the sum overflows `wgint`.

As defined for this example, the guard and the `caller_length` assertion
cover only `last_byte_pos - first_byte_pos + 1` with a zero first position.
The complete variant is therefore not a complete repair of caller-side
overflow.

## Variants

[make-variant.sh](make-variant.sh) creates each variant from a verified
tarball copy. It applies the [series](series/) with
`patch -p1 --fuzz=0 --forward --no-backup-if-mismatch --reject-file=-`. It
writes `TARGETCHECK-VARIANT.txt` (patch hashes and patch output) and
`TARGETCHECK-SOURCE.sha256` (every source file). It refuses any result that
differs from [SHA256SUMS](SHA256SUMS).

| Variant | Series | `src/http.c` SHA-256 |
| --- | --- | --- |
| `unpatched` | 0000 | `9e2cefe30a65bebe8465ef19f0349e519a38e9d5ae3bcc6b1f395620739654df` |
| `first-fix` | 0000, 0001 (43d3ba9) | `8527764144751816625f96cd7ac09adac2b704e9ab2ef4def90495da942b4314` |
| `complete` | 0000, 0001, 0002 (01ff771), 0003 (local guard) | `30d3908326b22abafbaec100869b523d4e7eb7ec5e6981b5960fd515480a7b86` |

The instrumentation files are identical in all variants:
`tests/content-range-cases.c` `c8b34e55fc3c71bc7b00c1568a163784dddc5c7b283a2e7bd3a1d9ecc05c7f6a`,
`tests/unit-tests.c` `0fad8bfc383a1bf5dee774037103f88742940ee9cb03cfa0fb49c14ef1da2893`,
`tests/unit-tests.h` `96fe6faa9b4a727f9f0a696819e6c1375d229bec034806140399ba422f06f5a8`.

## Build configuration

Each manifest builds a variant with four host commands: `./configure`, then
`make -C lib`, `make -C src libunittest.a` and `make -C tests unit-tests`.
The artifact is `tests/unit-tests`.

The configuration is the smallest one sufficient for these component tests.
It is **not** a vendor firmware configuration, and it reproduces no product's
feature set:

```text
--disable-nls --disable-iri --without-ssl --without-zlib --without-libpsl
--without-metalink --without-libuuid --disable-pcre --disable-pcre2
--disable-xattr --disable-ntlm --disable-opie --disable-digest
--disable-threads --disable-rpath --without-cares --disable-dependency-tracking
CFLAGS=-O1 -fsanitize=undefined -fno-sanitize-recover=undefined
CPPFLAGS= LDFLAGS=-fsanitize=undefined LIBS=
```

Native builds add `CC=gcc`. ARM builds add `--host=arm-linux-gnueabihf` and
`CC=arm-linux-gnueabihf-gcc`, with the Debian cross toolchain and sysroot
`/usr/arm-linux-gnueabihf`. They produce little-endian ARM32 EABI5 hard-float
binaries: ARM EABI version 5 with the hardware floating-point calling
convention. UndefinedBehaviorSanitizer (UBSan) runs with no recovery in every
variant and on both targets, so the first undefined behavior stops the test.
Each report records the exact argv of every build step.

## Cases and named diagnostics

Each TargetCheck test runs one named case, `tests/unit-tests CASE`, in its own
process, so a sanitizer abort identifies exactly one case. Output arguments
are compared only after a successful parse. Every run prints
`TYPE SIZES: long=N bits, wgint=64 bits`.

| Case | Category | Content |
| --- | --- | --- |
| `oversized-first-byte-pos`, `oversized-last-byte-pos`, `oversized-entity-length` | oversized decimal field | each field one past `WGINT_MAX`, then 30 digits; must be rejected |
| `max-endpoint-caller-boundary` | maximum endpoint / caller boundary | `bytes 0-9223372036854775807/*` rejected; `bytes 1-9223372036854775807/*` and `bytes 0-9223372036854775806/9223372036854775807` accepted. Only `last_byte_pos - first_byte_pos + 1` is checked: the accepted `bytes 1-9223372036854775807/*` still overflows the caller's later `contlen + contrange` when 1 is the resume offset (see the [residual](#residual-not-covered-by-the-guard)) |
| `large-offset-first-byte-pos`, `large-offset-last-byte-pos`, `large-offset-entity-length`, `max-representable-offsets` | preserved 64-bit offsets | values above 2^31 and 2^32, up to `WGINT_MAX`, must be accepted exactly |
| `historical-syntax` | historical accepted syntax | `bytes 0-999/1000`, `*` length, `bytes:` (JavaWebServer), no unit (old Netscape proxy), uppercase unit, extra spaces, leading zeros |
| `malformed-input` | malformed input | 17 rejected forms, such as empty input, `*/1000`, signs, spaces and missing fields |

The full boundary and compatibility suite is all ten cases. The
architecture-comparison subset is only the four preserved-offset cases, so the
local endpoint guard cannot confound the `long`-width regression.

A failed assertion prints its message on stdout, and the test exits 1:

| Assertion | Fails when |
| --- | --- |
| `range_accepted` | a valid header is rejected |
| `range_rejected` | an invalid header is accepted |
| `range_values` | an accepted header gives the wrong positions |
| `caller_length` | an accepted range's `last_byte_pos - first_byte_pos + 1` overflows. The caller's later `contlen + contrange` is not checked. |

The intended failures, each with exit 1, are:

- **`unpatched`:** each oversized field stops with a UBSan report. Natively,
  the first field reports
  `http.c:936:9: runtime error: signed integer overflow: 9223372036854775800 + 8 cannot be represented in type 'long int'`,
  and the other two fields report lines 942 and 955. On ARM32 the type is
  `'long long int'`.
- **`unpatched` (native and ARM32) and `first-fix` (native):**
  `content-range assertion range_rejected failed: case=max-endpoint-caller-boundary subcase=zero-first-max-last ...`.

  On ARM32, first-fix instead fails this case with
  `content-range assertion range_accepted failed: case=max-endpoint-caller-boundary subcase=one-first-max-last ...`,
  because its 32-bit `strtol()` also rejects `9223372036854775807`. No
  committed manifest runs this case for first-fix on ARM32. The case is
  outside the architecture-comparison subset because first-fix also fails it
  natively, for lack of the local guard.
- **`first-fix` on ARM32:**
  `content-range assertion range_accepted failed: case=large-offset-first-byte-pos subcase=above-2gib ...`.

## Manifests and the scenario matrix

Every manifest compares exactly two variants. Variant directories are
`ROW/VARIANT` below the TargetCheck root.

| Matrix | Manifest | Target | Baseline -> candidate | Suite | CLI exit |
| --- | --- | --- | --- | --- | --- |
| a | `a-native-security.json` | native | unpatched -> complete | full | 0 |
| b | `b-native-large-offset-first-fix.json` | native | unpatched -> first-fix | subset | 0 |
| b | `b-arm-large-offset-first-fix.json` | qemu-arm | unpatched -> first-fix | subset | 1 |
| b | `b-arm-large-offset-corrected.json` | qemu-arm | first-fix -> complete | subset | 0 |
| c | `c-arm-security.json` | qemu-arm | unpatched -> complete | full | 0 |
| tool | `missing-qemu.json` | qemu-arm, `/nonexistent/qemu-arm` | preflight only | subset | 2 |
| tool | `missing-sysroot.json` | qemu-arm, `/nonexistent/arm-linux-gnueabihf` | preflight only | subset | 2 |

In the `tool` rows TargetCheck stops at preflight, the checks it makes before
any command runs. Row a also provides the native half of row c: the complete
candidate and the full suite, natively.

Expected baseline failures use an exact exit plus the diagnostic marker.
Candidate expectations require exit 0 and `PASSED CASE`. The `missing-*`
manifests equal `b-arm-large-offset-first-fix.json` except for one target
field. [matrix.json](matrix.json) holds the additional assertions that
[democheck](democheck/main.go) applies to every run.
