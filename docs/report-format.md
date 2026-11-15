# Evidence reports

`internal/render.WriteJSON` and `WriteMarkdown` consume the same authoritative
`model.Report` and write only to the supplied `io.Writer`. `render.Renderer{}`
implements the shared renderer interface. The renderer does not run commands,
read the filesystem, assign statuses, compare markers or recalculate outcomes.

In short, JSON is authoritative and Markdown shows the same evidence. An
invalid report is rejected before any byte is written, untrusted text stays
text in both formats, and raw captured output never appears in either format.

JSON is authoritative and follows [report-v1.json](../schema/report-v1.json).
Both formats retain the declared coverage, each completed observation, explicit
`not_run` checks, expectations and actual normal exits, signal/failure causes,
target and ELF/ABI identities, source and artifact digests, exact planned/actual
argv, relative cwd, override names, capture counts/truncation and aggregate state.
A matched expected-negative baseline still shows its actual nonzero exit.

Representation validation rejects:

- unsupported schema versions;
- unknown outcome or check states;
- malformed metadata, digest, path, name or range fields;
- impossible combinations of normal observation and status;
- unsafe log references;
- invalid UTF-8.

Matched and mismatch reports must have the structurally complete coverage,
supported target metadata and declared manifest, input and artifact
identities defined by the shared contract; an empty collection cannot produce
a success report. Error and cancelled reports can retain incomplete coverage
and identities. Validation does not establish whether any test expectation was
correctly evaluated; it never converts or repairs a supplied verdict. A
rejected representation returns an error before writing bytes.

Ordering is baseline then candidate, build/test declaration indexes, and lexical
digest paths. Tool observations and environment override names have stable
ordering. Sorting copies the input slices, so rendering can run concurrently on
one report without changing caller data. Missing arrays serialize as empty arrays.
Exact valid Unicode string values survive JSON decoding unchanged. Markdown uses
quoted strings, visible control-character escapes and encoded punctuation so
untrusted names, diagnostics, arguments, backticks, HTML and newlines stay text.

Captures serialize only observed byte counts, truncation and optional relative log
references. Raw `Capture.Data` is excluded in both formats. The renderer accepts
only `logs/[A-Za-z0-9_-]+.stdout` or `.stderr`, with the suffix matching its stream;
it rejects traversal, URLs, spaces and injected Markdown rather than rewriting or
silently omitting evidence. Links use fixed stdout/stderr labels. The CLI owns
creating those files, permissions and atomic report completion; a reference does
not assert the renderer inspected its destination.

Environment values and inherited environments are absent from the report model.
Exact argv, diagnostics and separately persisted raw logs can contain sensitive
data; this format does not promise secret redaction or environment attestation.
A renderer or destination-write failure must propagate to the CLI as a report
failure, even when tests matched.

Focused validation in the pinned Linux environment:

```sh
go test -race ./internal/render/...
```

The committed JSON/Markdown goldens cover expected-negative matching, candidate
mismatch, build failure, timeout, cancelled partial execution and changed artifact
identity. Additional tests exercise exact string round trips, malicious text/log
paths, deterministic concurrent rendering, missing evidence and failing writers.
These are report-format tests, not actual ARM/Wget acceptance evidence.
