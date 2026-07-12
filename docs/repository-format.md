# Repository format version 1

Status: Unreleased draft

This document describes the currently implemented local repository prototype.
[ADR 0004](adr/0004-use-a-remote-first-sqlite-catalog.md) supersedes its remote
object set, layout, schemas, and publication sequence before the first release
with SQLite catalogs, immutable packs, and repository state generations. The
detailed specification must be updated before release, and repositories created
by development builds are not a compatibility boundary.

## Decision

Ressik stores immutable, encrypted objects in a local content-addressed
repository. A future destination copies only block, manifest, and commit
objects. It must never copy `repository.key`, daemon state, or lock files.

The version-one write sequence is:

1. Split regular files into fixed 4 MiB blocks.
2. Store every referenced encrypted block.
3. Store the encrypted snapshot manifest.
4. Store the authenticated commit marker last.

A snapshot is visible only when its commit marker exists and authenticates the
exact encrypted manifest. On POSIX filesystems, a crash before the last step can
leave unreachable objects but cannot durably publish a partial snapshot.
Garbage collection marks blocks from every committed manifest before removing
unreachable blocks, orphan manifests, and stale temporary objects. Ressik syncs
object files and their containing directories before publishing commit markers;
deletion syncs the commit directory before removing the corresponding manifest.

Directory-entry flushing and rename ordering are enforced on POSIX systems.
Windows flushes object contents before rename, but directory-entry flushing and
power-loss ordering are best effort because the platform does not expose the
same portable directory `fsync` primitive. After an interrupted Windows write,
Ressik reports an inconsistent committed snapshot instead of treating it as
restorable.

## Keys and object identifiers

Each repository owns one random 256-bit master key. BLAKE3 derive-key mode
creates independent block-ID and object-key bases using fixed, versioned
contexts. A third context derives a stable 128-bit repository ID, encoded as
32 lowercase hexadecimal characters. Copies opened with the same
`repository.key` have the same repository ID; independently keyed repositories
have unrelated IDs.

A block ID is keyed BLAKE3 over the object kind, format version, plaintext
length, and plaintext bytes. IDs are stable inside one repository for
deduplication, but unrelated repositories produce unrelated IDs for identical
content.

Each object's AES key is keyed BLAKE3 over its kind, version, and object ID.
Objects use AES-256-GCM with a random 96-bit nonce prepended by Go's
`cipher.NewGCMWithRandomNonce`. The clear header is authenticated as associated
data. On block reads, Ressik decrypts the bytes and recomputes the keyed block
ID.

## Object framing

Every encrypted object starts with this authenticated clear header:

```text
8 bytes   magic and format version: "RESSIK", 0x00, 0x01
1 byte    object kind: block, manifest, or commit
32 bytes  repository-scoped object ID
8 bytes   big-endian plaintext length
```

The header is followed by the GCM nonce, ciphertext, and authentication tag.
Manifest and commit IDs are random. Commit plaintext contains the BLAKE3 digest
of the encrypted manifest plus the plan ID, display name, creation time, Merkle
root, and statistics needed to list snapshots. The whole record is encrypted
and authenticated. Listing history therefore opens only small commit markers;
loading or restoring a snapshot verifies the marker against the complete
encrypted manifest.

## Destination object keys

Destinations store the exact authenticated local ciphertext without resealing
it. Provider-independent keys use slash separators and this versioned layout:

```text
ressik/v1/<repository-id>/blocks/<first-two-id-characters>/<id>.block
ressik/v1/<repository-id>/manifests/<id>.manifest
ressik/v1/<repository-id>/commits/<id>.commit
```

Adapters may prepend a configured destination prefix. They must upload every
referenced block before the manifest and publish the commit marker last. The
repository ID isolates independently keyed repositories sharing a bucket, but
it is an identifier rather than an authentication credential.

## Merkle encoding

The standalone `merkle` package uses the domain prefix
`ressik-merkle-v1\x00` and BLAKE3:

```text
empty  = H(domain || 0x00)
leaf   = H(domain || 0x01 || uint64_be(length) || payload)
parent = H(domain || 0x02 || left || right)
```

Trees are left-balanced by recursively splitting at the largest power of two
below the leaf count. The incremental builder preserves this shape with
`O(log n)` memory. Golden roots in `merkle/merkle_test.go` freeze the encoding.

Application leaf payloads are length-delimited and ordered:

- A file leaf contains the block index, plaintext block length, and keyed
  block ID.
- A directory leaf contains the raw child name, portable metadata including
  modification time, and child digest.
- A plan leaf contains the stable source ID, source-root kind and metadata, and
  source digest.

Directory entries and source IDs are sorted bytewise before hashing. Each
manifest and encrypted commit summary also carries a persistent configuration
ID, which namespaces plan history and retention when repositories are shared.
Absolute source paths exist only inside the encrypted manifest. A later scan
may reuse a regular file's digest and block references when its relative path,
kind, size, mode, modification time, filesystem identity, and change time still
match and every referenced block object exists. Changed files are read again.
This skips file-content reads. Except for globally ignored paths, Ressik still
walks and stats the source tree. Full mode rereads and hashes every included
regular file.

## Manifests and restore safety

Manifests use a bounded, versioned JSON schema inside the encrypted object.
Readers reject unknown fields, trailing data, duplicate source or entry paths,
absolute paths, drive and UNC paths, backslashes, traversal, unknown entry
kinds or symbolic-link target kinds, invalid block sizes, and file lengths
inconsistent with block refs.

Restore joins only validated source-relative paths beneath the requested
destination. It recomputes the complete manifest Merkle tree before writing.
Each block is authenticated before use, and the reconstructed file Merkle root
must match before an atomic no-replace rename publishes the file. The complete
staging tree is also published with no-replace semantics, so a raced destination
is never overwritten.

The explicit verification pass authenticates every commit, manifest, and
unique block, then recomputes file, directory, source, and plan Merkle roots.
The normal incremental pass performs only an object existence-and-size check
for unchanged file references so it does not turn every backup into a scrub.

## Retention

Retention considers committed snapshots only and runs after a newer snapshot
commits. `keep_last` and `keep_for` have union semantics; either rule protects a
snapshot, and the newest committed snapshot is always kept. Before deleting an
older snapshot, Ressik reloads the protected new manifest, recomputes its Merkle
tree, and authenticates every referenced block. Ressik removes a commit marker
before its manifest, then performs mark-and-sweep garbage collection while
holding the cross-process repository writer lock.

## Threat boundary

The repository format prevents a destination that lacks the master key from
reading block contents or manifest metadata and from testing guessed plaintext
against unkeyed content hashes. It does not hide object sizes, operation timing,
or equality of an opaque block reused inside one repository.

The local machine and its repository key are trusted. This version protects
the key with filesystem permissions rather than a platform credential vault.
Secure key export, credential-vault integration, remote rollback protection,
multi-host writers, and distributed locking are future format and product
work.
