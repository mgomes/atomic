# Repository format version 1

Status: Unreleased draft

This document describes the evolving first repository format. Some development
builds still publish JSON manifests, but the first supported format follows
[ADR 0004](adr/0004-use-a-remote-first-sqlite-catalog.md) and uses SQLite
catalogs, immutable packs, and repository state generations. Repositories
created by development builds are not a compatibility boundary.

## Decision

Atomic stores immutable, encrypted objects in a content-addressed repository.
Destinations copy only repository objects. They must never copy
`repository.key`, daemon state, local catalog working files, or lock files.

The version-one write sequence is:

1. Split regular files into fixed 4 MiB blocks.
2. Store every referenced encrypted block.
3. Store the encrypted snapshot catalog.
4. Store the authenticated commit marker last.

A snapshot is visible only when its commit marker exists and authenticates the
exact encrypted catalog. On POSIX filesystems, a crash before the last step can
leave unreachable objects but cannot durably publish a partial snapshot.
Garbage collection marks blocks from every committed catalog before removing
unreachable blocks, orphan catalogs, and stale temporary objects. Atomic syncs
object files and their containing directories before publishing commit markers;
deletion syncs the commit directory before removing the corresponding catalog.

Directory-entry flushing and rename ordering are enforced on POSIX systems.
Windows flushes object contents before rename, but directory-entry flushing and
power-loss ordering are best effort because the platform does not expose the
same portable directory `fsync` primitive. After an interrupted Windows write,
Atomic reports an inconsistent committed snapshot instead of treating it as
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
data. On block reads, Atomic decrypts the bytes and recomputes the keyed block
ID.

## Object framing

Every encrypted object starts with this authenticated clear header:

```text
8 bytes   magic and format version: "ATOMIC", 0x00, 0x01
1 byte    object kind: block=1, catalog=2, commit=3, or pack index=4
32 bytes  repository-scoped object ID
8 bytes   big-endian plaintext length
```

The header is followed by the GCM nonce, ciphertext, and authentication tag.
Catalog and commit IDs are random. Commit plaintext contains the BLAKE3 digest
of the encrypted catalog plus the plan ID, display name, creation time, Merkle
root, and statistics needed to list snapshots. The whole record is encrypted
and authenticated. Listing history therefore opens only small commit markers;
loading or restoring a snapshot verifies the marker against the complete
encrypted catalog.

## Snapshot catalog

Each snapshot catalog is a complete SQLite database materialized from a live
connection with SQLite's Online Backup API. Copying a database file or ignoring
its journal is not a valid catalog write. The completed database is bounded,
encrypted as one catalog object, and immutable after publication.

Catalogs set `application_id` to hexadecimal `0x5253494b` (`RSIK`) and
`user_version` to 1. Readers reject other values, failed SQLite integrity or
foreign-key checks, and missing or extra schema objects. Version 1 uses strict
tables:

- `snapshot` has exactly one row containing the snapshot and configuration IDs,
  plan metadata, creation time, root digest, chunk size, and statistics.
- `sources` contains each stable source ID, original path, and source root
  digest.
- `entries` contains portable source-relative paths, folded collision keys,
  parent paths, kind, mode, modification time, size, digest, link metadata, and
  the incremental change token.
- `blocks` contains each repository-scoped logical block ID and its plaintext
  length once.
- `entry_blocks` maps a file to its block IDs by zero-based ordinal.

Object IDs and Merkle digests are raw 32-byte BLOBs. Times are signed Unix
seconds plus nanoseconds. Modes use the unsigned 32-bit `os.FileMode` bit
pattern in a SQLite integer. Paths are UTF-8 text with binary collation and are
never normalized by the database. Block reference order comes only from the
stored ordinal; SQLite row order is never significant.

The catalog contains logical block IDs, not standalone object keys, pack IDs,
offsets, destination state, credentials, retry state, or mutable reference
counts. Reachability is derived from `entry_blocks`. Physical locations remain
rebuildable repository-state or local-cache data, so compaction can move an
authenticated block frame without rewriting any catalog.

## Immutable pack encoding

A pack is the bytewise concatenation of complete sealed block frames. Packs
have no outer encryption layer, so a frame remains independently authenticated
and can be copied verbatim during compaction. Writers initially target 4 MiB
packs; the version 1 index format accepts packs up to 64 MiB so tuning the target
does not require a format change. A frame that does not fit is stored in another
pack or as a standalone block object.

Each pack has a random 256-bit ID and an encrypted `pack index` object sealed
with that ID. The index plaintext uses this canonical big-endian encoding:

```text
8 bytes   magic: "ATOMICPI"
1 byte    index version: 1
32 bytes  pack ID
32 bytes  BLAKE3 digest of the complete pack
8 bytes   pack length
4 bytes   member count

For each member, sorted strictly by block ID:
32 bytes  repository-scoped block ID
8 bytes   offset within the pack
4 bytes   complete sealed-frame length
```

Member ranges must cover the pack exactly without gaps or overlaps. Readers
reject duplicate or unsorted block IDs and invalid ranges. Ordinary range reads
authenticate the encrypted index and the selected sealed block frame without
downloading the full pack. Full-pack reads, verification, and compaction also
reject a pack longer than 64 MiB or whose length or BLAKE3 digest differs from
its authenticated index. A pack is published before its index.

## Destination object keys

Destinations store the exact authenticated local ciphertext without resealing
it. Provider-independent keys use slash separators and this versioned layout:

```text
atomic/v1/<repository-id>/blocks/<first-two-id-characters>/<id>.block
atomic/v1/<repository-id>/catalogs/<id>.catalog
atomic/v1/<repository-id>/commits/<id>.commit
```

Adapters may prepend a configured destination prefix. They must upload every
referenced block before the catalog and publish the commit marker last. The
repository ID isolates independently keyed repositories sharing a bucket, but
it is an identifier rather than an authentication credential.

## Merkle encoding

The standalone `merkle` package uses the domain prefix
`atomic-merkle-v1\x00` and BLAKE3:

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
catalog and encrypted commit summary also carries a persistent configuration
ID, which namespaces plan history and retention when repositories are shared.
Absolute source paths exist only inside the encrypted catalog. A later scan
may reuse a regular file's digest and block references when its relative path,
kind, size, mode, modification time, filesystem identity, and change time still
match and every referenced block object exists. Changed files are read again.
This skips file-content reads. Except for globally ignored paths, Atomic still
walks and stats the source tree. Full mode rereads and hashes every included
regular file.

## Catalogs and restore safety

Catalog writers reject duplicate source or entry paths, case-fold collisions,
absolute paths, drive and UNC paths, backslashes, traversal, unknown entry kinds
or symbolic-link target kinds, invalid block sizes, conflicting block lengths,
missing directory parents, ordinal gaps, and file lengths inconsistent with
block references. Readers validate SQLite structure and stored invariants before
issuing selected-path queries and validate the complete logical snapshot when
loading it in full.

Restore joins only validated source-relative paths beneath the requested
destination. It recomputes the complete catalog Merkle tree before writing.
Each block is authenticated before use, and the reconstructed file Merkle root
must match before an atomic no-replace rename publishes the file. The complete
staging tree is also published with no-replace semantics, so a raced destination
is never overwritten.

The explicit verification pass authenticates every commit, catalog, and
unique block, then recomputes file, directory, source, and plan Merkle roots.
The normal incremental pass performs only an object existence-and-size check
for unchanged file references so it does not turn every backup into a scrub.

## Retention

Retention considers committed snapshots only and runs after a newer snapshot
commits. `keep_last` and `keep_for` have union semantics; either rule protects a
snapshot, and the newest committed snapshot is always kept. Before deleting an
older snapshot, Atomic reloads the protected new catalog, recomputes its Merkle
tree, and authenticates every referenced block. Atomic removes a commit marker
before its catalog, then performs mark-and-sweep garbage collection while
holding the cross-process repository writer lock.

## Threat boundary

The repository format prevents a destination that lacks the master key from
reading block contents or catalog metadata and from testing guessed plaintext
against unkeyed content hashes. It does not hide object sizes, operation timing,
or equality of an opaque block reused inside one repository.

The local machine and its repository key are trusted. This version protects
the key with filesystem permissions rather than a platform credential vault.
Secure key export, credential-vault integration, remote rollback protection,
multi-host writers, and distributed locking are future format and product
work.
