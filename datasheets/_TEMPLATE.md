<!-- Datasheet = three things only: the authoritative source, the Go envelope
     (signatures, no bodies), and implementation suggestions. No implementation. -->

# Datasheet: `<package>/<module>`

One line: what this module is and where it sits in the call stack.

**Validation vector:** `<name>.json` — the vector this module must match.

**Reference pinned at:** one of:

- `<full-40-character-git-SHA>` for a source implementation; or
- `capture SHA-256 <full-64-character-hash>` for a human-approved immutable
  capture source.

## Reference source (verbatim — authoritative)

For a source implementation, paste the cited source exactly.

For a capture authority, list the immutable raw file, byte/event boundary,
SHA-256, exact event line, and the JSON paths used by the vector. The raw JSONL
line at that boundary is the verbatim authority; do not duplicate secret
key/token content into the datasheet. A sanitized vector may replace opaque
contents with deterministic bytes only when it preserves their lengths,
positions, indexing, equality, and rotation relationships.

## Go envelope (signatures only)

The corresponding Go declarations — exported types and function signatures
with no bodies.

## Implementation suggestions (guidance, not authoritative)

Non-binding notes for whoever writes the bodies. Mark every design choice that
still needs human direction as `TODO(human)`.
