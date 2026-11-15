# TargetCheck report

- Schema version: 1
- Outcome: &#34;mismatch&#34;
- Tool: &#34;0&#46;1&#46;0&#45;test&#34;; commit: &#34;fixture&#34;; Go: &#34;go1&#46;27&#46;4&#34;
- Target: &#34;native&#34;; host OS: &#34;linux&#34;; host architecture: &#34;amd64&#34;
- Manifest SHA-256: &#34;0000000000000000000000000000000000000000000000000000000000000000&#34;; encoding: &#34;raw&#95;json&#34;

## Declared coverage

- Variant: &#34;baseline&#34;; build IDs: &#91;&#34;build&#45;0&#34;&#93;; test IDs: &#91;&#34;security&#34;&#44;&#34;compatibility&#34;&#93;
- Variant: &#34;candidate&#34;; build IDs: &#91;&#34;build&#45;0&#34;&#93;; test IDs: &#91;&#34;security&#34;&#44;&#34;compatibility&#34;&#93;

## Declared input identities

- Path: &#34;a&#45;config&#46;h&#34;; SHA-256: &#34;2222222222222222222222222222222222222222222222222222222222222222&#34;; size bytes: 10
- Path: &#34;z&#45;test&#46;c&#34;; SHA-256: &#34;1111111111111111111111111111111111111111111111111111111111111111&#34;; size bytes: 20

## Tool observations

- Role: &#34;compiler&#34;; path: &#34;&#47;usr&#47;bin&#47;gcc&#34;; version: &#34;gcc 12&#46;2&#46;0&#34;

## Variant &#34;baseline&#34;

- Cwd: &#34;baseline&#34;

### Artifact identities before testing

- Path: &#34;baseline&#47;parser&#34;; SHA-256: &#34;3333333333333333333333333333333333333333333333333333333333333333&#34;; size bytes: 100
  ELF class: &#34;ELF64&#34;; machine: &#34;EM&#95;X86&#95;64&#34;; endian: &#34;little&#34;; ABI: &#34;linux&#45;amd64&#34;

### Build checks

#### Check 0: &#34;build&#45;0&#34;

- Kind: &#34;build&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all declared markers matched&#34;
- Planned argv: &#91;&#34;gcc&#34;&#44;&#34;test&#46;c&#34;&#44;&#34;&#45;o&#34;&#44;&#34;parser&#34;&#93;
- Cwd: &#34;baseline&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 0; stdout marker: &#34;&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;gcc&#34;&#44;&#34;test&#46;c&#34;&#44;&#34;&#45;o&#34;&#44;&#34;parser&#34;&#93;
- Actual cwd: &#34;baseline&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 0
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/baseline-build-0.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/baseline-build-0.stderr)


### Test checks

#### Check 0: &#34;security&#34;

- Kind: &#34;test&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all declared markers matched&#34;
- Planned argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;security&#34;&#93;
- Cwd: &#34;baseline&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 3; stdout marker: &#34;&#34;; stderr marker: &#34;signed overflow&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;security&#34;&#93;
- Actual cwd: &#34;baseline&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 3
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/baseline-test-0.stdout)
- stderr: observed bytes: 15; truncated: false; log: [stderr log](logs/baseline-test-0.stderr)

#### Check 1: &#34;compatibility&#34;

- Kind: &#34;test&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all declared markers matched&#34;
- Planned argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;compatibility&#34;&#93;
- Cwd: &#34;baseline&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 0; stdout marker: &#34;&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;compatibility&#34;&#93;
- Actual cwd: &#34;baseline&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 0
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/baseline-test-1.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/baseline-test-1.stderr)


### Artifact identities after testing

- Path: &#34;baseline&#47;parser&#34;; SHA-256: &#34;3333333333333333333333333333333333333333333333333333333333333333&#34;; size bytes: 100
  ELF class: &#34;ELF64&#34;; machine: &#34;EM&#95;X86&#95;64&#34;; endian: &#34;little&#34;; ABI: &#34;linux&#45;amd64&#34;

## Variant &#34;candidate&#34;

- Cwd: &#34;candidate&#34;

### Artifact identities before testing

- Path: &#34;candidate&#47;parser&#34;; SHA-256: &#34;3333333333333333333333333333333333333333333333333333333333333333&#34;; size bytes: 100
  ELF class: &#34;ELF64&#34;; machine: &#34;EM&#95;X86&#95;64&#34;; endian: &#34;little&#34;; ABI: &#34;linux&#45;amd64&#34;

### Build checks

#### Check 0: &#34;build&#45;0&#34;

- Kind: &#34;build&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all declared markers matched&#34;
- Planned argv: &#91;&#34;gcc&#34;&#44;&#34;test&#46;c&#34;&#44;&#34;&#45;o&#34;&#44;&#34;parser&#34;&#93;
- Cwd: &#34;candidate&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 0; stdout marker: &#34;&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;gcc&#34;&#44;&#34;test&#46;c&#34;&#44;&#34;&#45;o&#34;&#44;&#34;parser&#34;&#93;
- Actual cwd: &#34;candidate&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 0
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/candidate-build-0.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/candidate-build-0.stderr)


### Test checks

#### Check 0: &#34;security&#34;

- Kind: &#34;test&#34;; status: &#34;mismatch&#34;
- Reason: &#34;normal exit 1 did not match expected exit 0&#34;
- Planned argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;security&#34;&#93;
- Cwd: &#34;candidate&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 0; stdout marker: &#34;&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;security&#34;&#93;
- Actual cwd: &#34;candidate&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 1
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/candidate-test-0.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/candidate-test-0.stderr)

#### Check 1: &#34;compatibility&#34;

- Kind: &#34;test&#34;; status: &#34;matched&#34;
- Reason: &#34;normal exit and all declared markers matched&#34;
- Planned argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;compatibility&#34;&#93;
- Cwd: &#34;candidate&#34;; timeout seconds: 5
- Environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Expected normal exit: 0; stdout marker: &#34;&#34;; stderr marker: &#34;&#34;
- Started: true; duration milliseconds: 12
- Actual argv: &#91;&#34;&#46;&#47;parser&#34;&#44;&#34;compatibility&#34;&#93;
- Actual cwd: &#34;candidate&#34;; environment override names: &#91;&#34;CFLAGS&#34;&#93;
- Actual normal exit: 0
- stdout: observed bytes: 0; truncated: false; log: [stdout log](logs/candidate-test-1.stdout)
- stderr: observed bytes: 0; truncated: false; log: [stderr log](logs/candidate-test-1.stderr)


### Artifact identities after testing

- Path: &#34;candidate&#47;parser&#34;; SHA-256: &#34;3333333333333333333333333333333333333333333333333333333333333333&#34;; size bytes: 100
  ELF class: &#34;ELF64&#34;; machine: &#34;EM&#95;X86&#95;64&#34;; endian: &#34;little&#34;; ABI: &#34;linux&#45;amd64&#34;

Identities cover declared files only. Outcomes describe the supplied expectations. Raw command logs can contain sensitive data.
