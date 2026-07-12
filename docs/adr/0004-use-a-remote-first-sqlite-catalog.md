# ADR 0004: Use remote SQLite catalogs and immutable packs

Status: Accepted

Date: 2026-07-12

## Decision

Ressik will make one configured destination authoritative for each repository.
Additional destinations will be one-way mirrors of that authority, not
independent repositories. Mirrors copy the same repository-relative Ressik keys
and bytes and never make their own retention, packing, compaction, or garbage
collection decisions.

Each snapshot will have an encrypted, immutable SQLite catalog containing its
filesystem entries, ordered logical block references, Merkle roots, and summary
metadata. Catalogs will reference repository-scoped BLAKE3 block IDs, never
physical pack locations. A snapshot commit will authenticate each catalog.

Every logical change to the authority's active object set will publish an
immutable, authenticated repository state generation last. The generation
identifies its authority epoch and predecessor and authenticates the complete
active set of snapshot commits and pack indexes. Physical cleanup of objects
retired by that generation follows afterward. This single chain is the atomic
boundary for backup, retention, compaction, mirroring, and recovery; it is not
reconciled with another writer.

Ressik will store complete sealed blocks either as standalone objects or inside
immutable packs. Within each backup generation, newly discovered small blocks
will be emitted in Merkle-tree traversal order and collected into packs targeting
roughly 4 MiB. Merkle location is only a placement hint: it is not part of block
identity, and an existing deduplicated block will not move merely to improve
locality.

Retention thinning and any future storage-budget enforcement will use the same
garbage collection path. Ressik will derive reachability from retained snapshot
catalogs, delete fully dead objects, and compact sufficiently fragmented packs
by copying their live sealed frames into replacement packs. Snapshot catalogs
will not change when a block moves.

The local SQLite database will be a rebuildable cache and working index, not the
authority. Local encrypted payload staging and cached payload data will remain
bounded to at most 5% of the protected bytes for the operation. Catalog and
other rebuildable metadata will have a separate bounded allowance.

## Context

The current engine keeps a complete encrypted repository locally before a
destination can copy it. An initial backup of N unique bytes therefore requires
approximately N additional local bytes even when the intended durable copy is
remote.

One remote object per small file or tail block creates excessive request and
metadata overhead. Packing reduces that overhead, but completed object-store
objects cannot be edited or truncated. A pack can therefore contain both live
and unreachable blocks after snapshots expire.

Deduplication also makes retention a graph problem. A block may be referenced by
many files and snapshots, and a mutable reference counter can become stale after
a crash or cache rebuild. Physical deletion must be justified by the retained
snapshot graph.

Allowing every destination to choose its own pack layout and deletion history
would require reconciliation among several writable repositories. Ressik needs
multiple durable copies, but it does not need multiple concurrent authorities.

## Authority and mirrors

The authority is the only destination that accepts new snapshots or changes the
active object set. A writable authority must provide strongly consistent reads
and listings for repository state generations. An adapter that cannot establish
that property may be used as a mirror but not as the authority.

A mirror authenticates every source object and synchronizes one committed
authority generation at a time:

1. Copy new standalone blocks, packs, and pack indexes.
2. Copy the encrypted snapshot catalogs.
3. Copy snapshot commits.
4. Copy the authenticated state generation last.
5. Apply deletions only after the generation that retired those objects is
   active on the mirror.

An exact mirror has identical active repository-relative Ressik keys and bytes.
Provider version IDs, ETags, delete markers, and other service metadata need not
match. A lagging mirror remains restorable at its last complete generation and
may temporarily retain objects that the authority has deleted.

If the authority is unavailable, backup, retention, and compaction stop. Restore
and verification may use a complete mirror. Promoting a mirror is an explicit
recovery operation that verifies every block reachable from its active
generation and starts a new random authority epoch. Before promotion, the
operator must stop the former writer and revoke or make read-only its destination
access; Ressik aborts if that external fence cannot be established. The promoted
state is never merged with a returning authority, which must instead be replaced
from the new one. Changes newer than the mirror's last complete generation may
be lost.

Ressik assumes one mutating process at a time. A repository writer lock
serializes backup, retention, compaction, and mirror promotion on one machine.
Ressik does not provide distributed locking or automatic fencing.

## Snapshot publication and physical resolution

The SQLite catalog excludes credentials, retry state, destination state, and
physical block locations. Ressik will materialize it with SQLite's Online Backup
API rather than copying a live database and its journal.

A snapshot becomes visible at the authority only after:

1. Uploading every new standalone block and pack required by the snapshot.
2. Uploading each new pack's encrypted and authenticated index after its pack.
3. Uploading the encrypted SQLite catalog.
4. Uploading its authenticated snapshot commit.
5. Publishing the next authenticated repository state generation last.

Each staged payload object may be released as soon as the authority durably
acknowledges its exact bytes. Mirrors later source that ciphertext from the
authority, so a snapshot larger than the local payload allowance does not need
to remain staged until its commit.

A pack index binds its pack ID, digest, length, and the block ID, offset, and
sealed length of each member. Readers reject duplicate block IDs, overlapping or
out-of-bounds ranges, and frames whose authenticated headers do not match the
requested logical block.

The local database may cache logical-to-physical mappings and reference counts.
Those rows can accelerate deduplication and garbage collection, but they can be
rebuilt from the active state generation, its committed catalogs, standalone
objects, and pack indexes. A logical block may temporarily have more than one
valid physical location during compaction.

Published packs are immutable. Each member is a complete, independently
authenticated sealed block frame whose authentication does not depend on its
pack ID or offset. Compaction can therefore copy ciphertext frames verbatim
without exposing plaintext or changing their logical block IDs.

## Retention and compaction

Retention keeps the existing policy semantics: `keep_last` and `keep_for` form a
union, and the newest committed snapshot is always retained. A future storage
budget policy must define whether it overrides those protections before it
ships. Either policy first selects retained snapshots; neither makes a reachable
block directly deletable.

Thinning first constructs a proposed state without the expired snapshot commits
and recomputes every logical block reachable from its retained catalogs. The new
state also excludes pack indexes with no live members. Cached reference counts
are an optimization, not deletion authority, and each pass rebuilds the mark
rather than resuming a cached set. Only after the proposed generation is durable
may physical cleanup begin:

- An unreachable standalone block may be deleted.
- A pack with no live members may have its retired index and pack deleted.
- A partially live pack remains readable until replacement locations for all of
  its live members are committed.

Ressik will consider compaction after retention or budget enforcement rather
than after every file change. A pack becomes a candidate only when both its dead
byte count and dead-byte ratio exceed configured thresholds. Exact defaults are
an implementation choice; they do not affect repository correctness.

Compaction combines authenticated live frames from one or more fragmented packs
into new packs. It proceeds in this order:

1. Upload replacement packs.
2. Upload their authenticated indexes.
3. Publish a new state generation that activates the replacement indexes and
   retires the old indexes.
4. After active readers have drained, delete the old indexes and packs.

A reader that races with deletion refreshes the active generation and retries. A
crash before the new generation is published leaves unreachable replacement
objects; a crash after publication leaves redundant old objects. Both states are
safe to clean during the next garbage-collection pass.

Compaction requires temporary remote headroom. Budget enforcement deletes fully
dead objects first and rewrites fragmented packs incrementally, bounding the
extra storage to a small number of replacement packs. If the provider cannot
supply that headroom, Ressik reports that compaction is blocked rather than
deleting an old pack first.

## Restore and recovery

Restore starts from an authenticated state generation, snapshot commit, and
catalog at the authority or a complete mirror. It queries only the selected
paths, resolves their logical block IDs through standalone objects and active
pack indexes, and fetches only the required objects or byte ranges. It
authenticates every catalog, index, and block and recomputes the existing Merkle
roots before publishing restored files. The new catalog preserves the existing
path validation, traversal rejection, and atomic no-replace publication rules.

Losing local state does not lose repository authority. Ressik rebuilds its cache
from the latest valid state generation, committed catalogs, and active pack
indexes at the configured authority. Garbage collection derives liveness from
that authenticated inventory, not from objects missing in a provider listing.
It never hydrates a full local repository merely to restore or rebuild the cache.

Disaster recovery still requires the separately protected `repository.key`.
This decision does not upload the key, destination credentials, or plaintext
catalog data.

## Local storage ceiling

The 5% ceiling covers encrypted block and pack staging, retryable partial
uploads, and cached remote payload. One cross-process quota ledger per cache root
counts existing allocated payload bytes and outstanding reservations. Ressik
reconciles it with crash leftovers at startup and each operation start, and
reserves the worst-case simultaneous temporary and final allocation before
writing. Concurrent operations share the smallest active ceiling. Before
lowering that ceiling, Ressik evicts rebuildable cache; if non-evictable staging
would still exceed it, the new operation must use no disk payload allowance,
wait, or fail. Ressik never activates a ceiling that is already exceeded.

Backup derives its ceiling from preflight included-file bytes. Restore and
serial verification use the selected snapshot's authenticated plaintext
statistics. Recovery, index rebuild, and compaction have no protected-byte
measurement and therefore stream payload through bounded memory without a disk
payload cache. Configuration may lower but not raise these ceilings, and an
operation stops before publishing a snapshot rather than exceed its allowance.

SQLite working files, encrypted catalogs, physical-location rows, and other
rebuildable metadata use a separate counter and configured cap in the same
reservation ledger because metadata for many empty or tiny files can exceed any
percentage of payload bytes. Ressik reconciles both counters at startup and each
operation start and fails a metadata write if it cannot measure allocated bytes
conservatively. The encrypted catalog must fit this allowance as a replayable
single-object upload; otherwise backup fails without publishing a new state
generation. Ressik reports payload and metadata usage separately.

## Repository format

No Ressik repository format has been released. This decision therefore extends
the initial `ressik/v1/<repository-id>/...` layout from ADR 0003 instead of
creating a parallel namespace or migration path. The first supported format will
include the catalog, pack-index, repository-state, and commit schemas described
here while retaining the existing repository-scoped block IDs, key derivation,
and sealed block frames.

Development repositories created before the first release are not a
compatibility boundary and may need to be recreated. The repository-format
specification must be updated to incorporate this decision before the format is
released.

This decision also amends ADR 0002 to require byte-range `GetObject` for
efficient pack reads and, for versioned providers, paginated object-version
listing and version-specific deletion. Roughly 4 MiB packs and metadata-capped
catalogs use ordinary `PutObject`; multipart upload remains a separate decision.
An adapter must prove that deletion reclaims provider versions before
storage-budget enforcement may count those bytes as reclaimed.

## Consequences

Remote backups no longer require a second full local copy. SQLite makes
individual-file restore and reachability directly queryable. Packing amortizes
remote operations for small files, while logical-to-physical indirection permits
compaction without rewriting snapshot catalogs. A mirror copies one physical
layout instead of maintaining an independently reconciled repository.

The implementation now owes a portable SQLite integration, an updated repository
format specification, authenticated repository-state generations and pack
indexes, byte-range reads, ordered mirror synchronization, copy-on-write
compaction, and failure-injection tests around every publication boundary.
Rebuilding a lost cache may require opening many catalogs and indexes. Publishing
each snapshot uploads a complete SQLite catalog even when most metadata is
unchanged.

The authority is an availability dependency. Automatic failover is unavailable,
and an asynchronous mirror can lose the authority's newest state. Compaction
consumes reads, writes, and temporary remote storage. The 5% local payload limit
can reduce throughput when a source or destination is slow.

Once packs have been published, disabling new packing does not remove the need
to read existing packs. They must remain supported or be drained through
compaction.

## Alternatives considered

- A complete local mirror simplifies offline operation but requires
  approximately the protected data size in additional local storage.
- Independently writable destinations improve write availability but require
  conflict resolution for retention, placement indexes, and garbage collection.
- One remote object per logical block makes reclamation simple but performs
  poorly for repositories containing many small files.
- Mutable packs avoid copying live members, but object stores replace complete
  objects and an interrupted overwrite could destroy retained data.
- Packing strictly by Merkle location improves locality, but deduplicated blocks
  may have multiple parents and ancestor hashes change with their descendants.
  Ressik therefore uses traversal location only as a placement hint for new
  blocks.
- Never compacting partially live packs avoids rewrite cost but allows dead bytes
  to grow without bound.

## Non-goals

- Automatic authority election or multi-writer repositories.
- Cross-repository or cross-user deduplication.
- Completing backups while the authority is unavailable and staging is full.
- Uploading `repository.key`, credentials, or plaintext catalog data.
- Fixing permanent pack-size, fragmentation-threshold, metadata-cap, or
  reader-drain defaults in this ADR.
- Choosing how a future storage budget interacts with time/count retention.

## References

- [ADR 0002: Use a minimal S3-compatible client](0002-use-a-minimal-s3-client.md)
- [ADR 0003: Define a repository transfer protocol](0003-define-repository-transfer-protocol.md)
- [Repository format version 1](../repository-format.md)
- [SQLite Online Backup API](https://www.sqlite.org/backup.html)
