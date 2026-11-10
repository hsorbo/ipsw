# RIDIFF10 patch application

On non-Darwin hosts with cgo, `RawImagePatch("", patch, output, verbose)`
uses the portable Go applier. Darwin with cgo continues to use Apple's
`libParallelCompression`. `Apply(patch, output)` explicitly selects the
portable implementation on any cgo host, including macOS.

Supported patches are full replacements, with one variant, pbzx (XZ or
stored) sections, and decmpfs type 14 (LZBITMAP) compressed forks with flags
exactly 1. Base images, delta controls, pbzm/LZRAVEN, copied forks, other
compression types, and chunks requiring reclamation of earlier padding
return errors. Builds without cgo return a clear encoder requirement error.

The applier validates the header, extent ownership and bounds, chunk tables,
and control lengths before reconstructing the image. It decodes pbzx blocks
and encodes 64 KiB fork chunks in bounded parallel batches, writes across
fork extents in metadata order, and fills gaps in image order. The stream's
SHA-256-of-piece-digests resets its 8 MiB piece boundary at each fork and
at the combined gap stream. Output stays in a temporary file beside the
destination until the digest and stream length match, then is renamed.
Metadata and retained controls are each limited to 256 MiB.

The reconstruction rules follow the supplied *Applying RIDIFF10 patches
without macOS* format guide (`spec.md`, `lzbitmap.md`, and `testing.md`).
The encoder comes from retrowave3/apple-compressions, with its MIT licence
and pinned revision recorded in `internal/lzbitmap/README.md`. Its small
cgo wrapper is internal to ipsw; the existing lzfse-cgo dependency is unchanged.
The in-memory `ParseRawImageDiff10` API remains available; it now
returns parsed image metadata in `Image` and does not write diagnostic files.

Run from the repository root:

```sh
go test -race ./pkg/ota/ridiff
CGO_ENABLED=0 go test ./pkg/ota/ridiff
go test ./internal/lzbitmap/...
```

On macOS the encoder tests compare 3,000 deterministic generated inputs,
at destination capacities 65,536 and input size plus 1,024, against
libcompression selector 0x702. Synthetic patch tests cover layout, padding,
stored chunks, shifts, reversed extents, multiple forks, digest boundaries,
malformed input, and preservation of existing output on failure.

Real Apple patches are external, optional integration fixtures:

```sh
RIDIFF_TEST_PATCH=/path/to/cryptex-app go test -v ./pkg/ota/ridiff -run '^TestRealPatch$'
```

The test recognizes the metadata digests of Tahoe 26.7.1's cryptex-app and
cryptex-system-x86_64 and compares their complete image SHA-256 values with
the format guide's vectors. For other patches, set `RIDIFF_TEST_SHA256` to
an independently obtained image hash. A skipped integration test does not
establish real-patch or Linux runtime parity.

AI assistance: OpenAI Codex implemented the Go applier, encoder wrapper,
fixtures, and this documentation from the supplied format guide. The
vendored C encoder is upstream code. Human review and verification are
required before an upstream submission under the project's AI policy.
