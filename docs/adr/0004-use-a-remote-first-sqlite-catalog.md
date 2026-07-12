# ADR 0004: Use remote SQLite catalogs and immutable packs

Status: Accepted

Date: 2026-07-12

## Decision

Ressik will make committed destination objects authoritative for backup data
instead of retaining a complete local repository. Each snapshot will have an
encrypted, immutable SQLite catalog containing its filesystem entries, ordered
logical block references, Merkle roots, and summary metadata. The snapshot's
commit marker will authenticate the exact catalog and be published last.

Snapshot catalogs will reference only repository-scoped BLAKE3 block IDs. They
will not contain physical object locations. Large sealed blocks may be stored as
standalone objects. Small sealed blocks may be combined into immutable packs,
with a separate encrypted and authenticated pack index mapping each logical
block ID to its byte offset and sealed length. A logical block may temporarily
have more than one valid physical location.

The local SQLite database is a rebuildable cache and working index, not the
authority. It may materialize reference counts, remote acknowledgements, and
physical locations for speed. Correctness comes from authenticated snapshot
commits, snapshot catalogs, standalone objects, and pack indexes stored at the
destinations.

Local encrypted payload staging and cached object data will have a hard ceiling
of 5% of the current run's preflight measurement of included regular file bytes;
configuration may lower but not raise it. Five percent is small enough that
no one provisions disk for a second copy and large enough to keep uploads
streaming; a raisable ceiling would quietly regrow the local mirror this
decision removes. Ressik will reserve budget before
writing, apply backpressure, and leave a snapshot uncommitted rather than exceed
the ceiling. Catalog and rebuildable control metadata are bounded separately
because a tree of empty files can contain more metadata than any percentage of
its plaintext bytes.

Published packs will never be modified in place. Ressik will reclaim
fragmentation with copy-on-write compaction: publish a replacement pack and its
index before retiring the old index and, after a safety grace period, the old
pack. Compaction may fill the replacement with new small blocks from the current
backup. Snapshot catalogs do not change when a block moves.

## Context

The current backup engine writes every encrypted block into a permanent local
repository before committing a manifest. This makes local-only backup,
verification, retention, and restore straightforward, but the first backup of
N unique bytes requires approximately N additional local bytes. A cloud-first
backup application should not require enough free space for a second complete
copy of the protected data.

Ressik also supports multiple destinations. One destination may accept an
object while another is unavailable. Retaining every pending object locally
until every destination recovers makes local usage unbounded. After one
destination durably stores authenticated ciphertext, Ressik can release its
staged copy and later stream that ciphertext to a lagging destination through
bounded memory.

Small files create a different scaling problem. One remote object per tiny file
produces excessive request, listing, and metadata overhead. Packing reduces
that overhead, but S3 and compatible services cannot delete or overwrite a byte
range in a completed object. Dead pack members therefore require temporary
fragmentation or a new copy-on-write pack.

Retention is a graph problem. The same logical block may be referenced by many
files and retained snapshots, and one pack may contain both live and dead
blocks. A mutable counter alone is insufficient evidence for deletion after
crashes, interrupted replication, cache repair, or partial destination failure.

## Format transition

This decision supersedes ADR 0003's remote publication sequence and key layout
for new remote-first snapshots. Those snapshots use a separate
`ressik/v2/<repository-id>/...` destination namespace, distinct catalog,
pack-index, replication-waiver, retention-removal, and commit schemas, and
distinct authenticated object kinds. Version 1 keeps its existing meaning and remains readable; a
reader must never infer an object's schema from current configuration or
reinterpret a version 1 manifest as a version 2 catalog.

Migration is copy-and-verify, not an in-place rewrite. Ressik authenticates a
version 1 snapshot, writes its version 2 objects under the separate namespace,
and publishes the version 2 commit last. It leaves the version 1 snapshot intact
until every selected snapshot is committed and verified in version 2 and the
user explicitly removes the old copy. Existing sealed block frames may be
copied into version 2 standalone objects or packs when their version 1 semantics
remain unchanged.

This decision also amends ADR 0002's deliberately small client surface.
Version 2 restore requires ranged GetObject reads so single pack members can
be fetched without downloading whole packs. Objects that exceed local
staging or a provider's single-request size limit are sent with multipart
uploads, which keep memory bounded while every part still signs its own
SHA-256 payload; SigV4 streaming payloads and presigned URLs remain
excluded. Recovery-scale listing composes the existing single-page
ListObjectsV2 operation and needs no new client surface.

## Snapshot publication

The backed-up SQLite catalog contains one snapshot's logical state. It excludes
credentials, retry timers, daemon status, destination acknowledgements, and
physical pack locations. Ressik will create the immutable catalog through
SQLite's snapshot or online backup facilities rather than copying a live
database file and its journal.

A catalog can be far larger than a version 1 manifest, and version 1 seals
each object in one in-memory operation. Version 2 therefore defines a sealed
framing for large objects that encrypts, authenticates, uploads, and reads
them through bounded memory; the concrete framing belongs to the version 2
format specification.

For each destination, a snapshot becomes visible only after this sequence:

1. Upload every new standalone block and pack required by the snapshot.
2. For each new pack, upload its bytes before its authenticated pack index.
3. Upload the encrypted SQLite snapshot catalog.
4. Upload the authenticated snapshot commit marker last.

The commit records the snapshot ID, catalog digest, and stable IDs of the
destinations required when the snapshot was captured, and it keeps the
version 1 commit's summary role: plan identity, display name, creation time,
Merkle root, and the statistics needed to list snapshots without downloading
catalogs. A destination's stable ID is a random value minted when the
destination is attached and recorded at the destination as well as in
configuration, so recovery on a new machine re-matches configured
destinations to recorded obligations, and waivers accept historical IDs that
no longer appear in configuration. A snapshot may be
complete on one destination while another is pending. Once at least one
physically complete destination can supply every required object, local staged
payload may be released. Until every required destination is physically complete
or has an authenticated waiver, the snapshot's commit, catalog, indexes, and
physical blocks remain pinned on at least one physically complete destination.

Removing a required destination is an explicit authenticated repository
operation. It publishes a replication-waiver record naming the snapshot and
destination to every remaining complete destination before clearing that pin.
Editing configuration or losing credentials never implies a waiver, and
Ressik refuses to remove a destination while it is the last physically
complete source for any snapshot with outstanding replication obligations;
abandoning the data itself is a separate, explicitly destructive snapshot
deletion rather than a waiver. Recovery applies only authenticated waiver
and retention-removal records, so an unavailable destination pins data until
it catches up, the user deliberately abandons delivery, or retention has
removed the snapshot.

If local cache and delivery state are lost, Ressik lists snapshot commits and
pack indexes at the configured destinations and authenticates their catalogs.
The union of valid commits is reconstructed conservatively. The same snapshot
ID with different catalog digests is corruption: snapshot IDs are unique per
capture, a catalog is sealed exactly once and never resealed, and every
destination receives byte-identical catalog ciphertext, re-fetched from a
complete destination when local staging is gone. Physical locations are rebuilt
and checked separately for each destination; the presence of an authenticated
commit alone does not prove that destination is complete. Recovery and
garbage collection treat listings as complete, so version 2 destinations
must provide strongly consistent read-after-write and list-after-write
visibility; an eventually consistent destination is unsupported.

A recovered destination becomes the only replication source, or authorizes
deletion of another copy, only after every reachable logical block resolves on
that destination and its sealed frame authenticates. During active publication,
a verified acknowledgement for the exact uploaded bytes establishes that
object; a rebuild after losing those acknowledgements performs a full scrub.
Ordinary block reuse needs no scrub: a new snapshot may reuse a logical block
when an authenticated pack index or standalone-object listing places it at a
required destination, and the explicit verification pass remains the detector
for provider-side corruption, so evicting acknowledgement rows never forces
re-uploads. A lagging destination may temporarily retain a snapshot that
current retention would remove; this can consume extra remote storage but
cannot hide or delete a retained snapshot.

After restart, a publication with no valid commit at any destination is
abandoned. Its local remnants are reconciled against the storage ceiling before
new work begins. Uploaded standalone blocks, packs, indexes, and catalogs are
unreachable orphans until a later backup reuses them or garbage collection
removes them.

## Physical object resolution

Standalone block keys remain deterministic from their logical block IDs. A pack
index binds its pack ID, exact pack digest and length, and a sorted set of unique
block IDs, offsets, and sealed lengths. Readers reject duplicate IDs,
overlapping or out-of-bounds ranges, and frames whose authenticated headers do
not match the requested logical block.

Pack indexes are availability markers. A resolver may use any authenticated
standalone object or pack index that contains the requested block. Local SQLite
rows cache these choices with the destination ID as part of every key, but can
be rebuilt by listing and opening pack indexes. An optional compact index
checkpoint may accelerate a large rebuild, but it is not required for
correctness.

Destinations may compact independently because snapshot catalogs contain no
pack IDs. A lagging destination may therefore use a different physical pack
layout for the same logical snapshot.

## Concurrency

Version 2 assumes exactly one mutating writer per repository namespace at a
time. On one machine, an exclusive lock serializes backup, retention,
compaction, waivers, and migration, as the version 1 repository lock does
today. Ressik does not coordinate writers across machines: operating two
machines against the same namespace is unsupported and can destroy data the
other writer still needs, and remote coordination or detection is future
work. Restore and verification never mutate a destination and may run
anywhere; a concurrent writer's retention can only make an in-progress
restore fail cleanly, never publish a partial tree.

As defense in depth against imperfect deployments, garbage collection never
deletes a destination object younger than a configured minimum age that
exceeds the longest plausible publication. This generalizes the pack grace
period to standalone blocks, catalogs, indexes, and control records.

## Retention and compaction

Before deleting anything, Ressik derives the retained root set from valid
commits, authenticated replication waivers, the retention policy, and pending
replication. A snapshot remains a replication root while any required
destination is neither authenticated-waived nor verified physically complete;
commit presence is necessary but not sufficient. At least one physically
complete source remains pinned. Only a snapshot outside that root set is
expired; Ressik then removes its commit marker before its catalog and marks
logical blocks from the remaining roots. Before removing any commit marker,
expiry publishes an authenticated retention-removal record naming the
snapshot to every reachable required destination. Recovery treats that
record as ending the snapshot's replication obligations, so an expiry
interrupted between destinations resumes as a removal instead of
resurrecting the snapshot as a replication root that would re-replicate
already-deleted blocks.

- An unreachable standalone block may be deleted.
- A pack with no live members may have its index retired and then be deleted.
- A partially live pack remains readable until copy-on-write compaction safely
  publishes another location for every live member.

Compaction is opportunistic and bounded. Ressik selects sufficiently fragmented
packs, copies their live sealed frames verbatim into a new pack, and may add new
small blocks until the target pack is full. It authenticates every source frame
before publication and clears any transient plaintext.

The safe replacement sequence at one destination is:

1. Upload the replacement pack.
2. Upload its authenticated pack index, making the new locations discoverable.
3. Retire the old pack index so new resolvers stop selecting it.
4. Wait a bounded grace period for readers that already selected the old pack.
5. Delete the old pack.

A restore that loses a race with deletion refreshes physical locations and
retries from the replacement. A crash before step 2 leaves an unreachable new
pack. A crash after step 2 leaves duplicate valid locations. A crash after step
3 leaves an unreachable old pack. Garbage collection can repair all three
states without losing a committed snapshot.

Retirement removes the old index from the active index namespace. On recovery,
Ressik treats every pack without a discoverable index as newly retired and
waits a fresh full grace period before deleting it. Losing the locally recorded
discovery time restarts the grace period. The same conservative rule protects a
new pack left by a crash before its index was published.

A lost index and a completed retirement look identical in a listing, so
deleting an indexless pack additionally requires that every live logical
block resolve to a surviving authenticated location on that destination.
While any live block is unresolvable, garbage collection halts with an
integrity error and preserves indexless packs as repair material.

Provider retention or object-lock policy may delay physical deletion. Ressik
records deletion as pending and retries after the provider permits it.

## Restore and verification

Restore begins with the selected snapshot's authenticated commit and SQLite
catalog at any destination where that snapshot is complete. It queries the
selected path's ordered logical block references, resolves each block from that
destination's current standalone objects and pack indexes, and fetches only the
required objects or pack ranges. A bounded local LRU may cache downloaded
ciphertext but is not required for correctness.

Ressik authenticates every catalog, pack index, and block before use. Existing
Merkle verification remains the end-to-end check over reconstructed files,
directories, sources, and plans. A restore never hydrates a full local
repository first.

Disaster recovery still requires the separately recovered `repository.key`.
This decision does not upload it or introduce a passphrase-wrapped remote copy.

## Local storage ceiling

The 5% ceiling covers encrypted block and pack staging, partial uploads retained
for retry, and cached remote object data. Before writing any of those bytes to
disk, Ressik performs a current preflight walk with the configured ignores and
sums the logical sizes of included regular files. The payload budget is at most
5% of that measurement, rounded down; until preflight finishes, the on-disk
payload budget is zero. That measurement is the contract for the run: later
growth or shrinkage does not change the budget, and growth therefore makes it
more conservative.

Every filesystem write in those categories reserves its complete worst-case
simultaneous footprint first, including temporary and final copies used by an
atomic replacement. A cross-process spool lock excludes other writers for the
lifetime of each reservation. On startup Ressik measures all existing spool and
cache files, counts crash leftovers before granting a new reservation, and
deletes or validates them under the same lock. Direct upload with bounded memory
is used when an object cannot fit in the remaining disk budget.

Operations without a preflight measurement do not inherit a stale one.
Restore, verification, and cache rebuild bound their downloaded ciphertext
with fixed configured caches instead, so a machine that only restores never
computes the payload ceiling.

When all destinations are unavailable, Ressik cannot both preserve an
arbitrarily large unfinished snapshot and obey the ceiling. It stops before the
next reservation and reports that destination delivery is blocking progress.
Compaction uses the same reserved staging or direct-upload path and defers when
neither can complete safely.

Catalog and rebuildable control metadata have a separate configured hard byte
cap. Unlike the payload ceiling, this cap may be raised, because a larger
source tree legitimately needs a larger active catalog. The working database, WAL and shared-memory files, SQLite backup output,
encrypted catalog, atomic-write temporaries, delivery acknowledgements, and
physical-location rows all reserve and count against that allowance. Rows that
grow with repository history are evictable and remotely rebuildable. Ressik
evicts rebuildable history and falls back to remote queries and fuller scans
before failing clearly if the active snapshot catalog itself cannot fit.
Metadata for empty or tiny files can exceed 5% of their content bytes, so total
Ressik disk use can exceed 5% by this explicitly bounded allowance; the backup
payload cache cannot.

## Consequences

Initial backups no longer require a second local copy of all protected data.
SQLite makes individual-file restore and logical reachability directly
queryable, while logical-to-physical indirection allows pack compaction without
rewriting snapshot metadata. A lost cache can be reconstructed from remote
commits, catalogs, and pack indexes.

The implementation now owes a portable SQLite integration, versioned schemas,
consistent checkpoint creation, per-destination reconciliation, range reads,
authenticated pack indexes, copy-on-write compaction, and failure-injection
tests across every publication boundary. Cache rebuild may require listing and
opening many pack indexes. Compaction temporarily consumes additional remote
bytes and transfer operations. Restoring one path still requires downloading
that snapshot's catalog, and publishing a snapshot uploads a complete catalog
even when most file metadata is unchanged.

Every run also pays a preflight walk over the included sources before its
first payload write, which delays capture start on large or slow
filesystems. Backpressure couples capture speed to destination throughput,
so a first backup of a large source over a slow uplink holds its capture
window open longer and is more exposed to source mutation before commit; an
abandoned attempt leaves reusable uploaded blocks, so repeated runs converge
instead of starting over. Catching up a lagging destination streams full
history through the client and pays provider egress, independent compaction
multiplies pack indexes and transfer work by destination count, and a
migrating repository stores version 1 and version 2 copies until the user
removes the old one.

Version 2 requires a new repository-format specification and an explicit
version 1 migration command before it can replace the current engine. Version 1
read support remains until migrated snapshots have been verified and
deliberately removed.

Plans need at least one durable destination. A local filesystem may implement
the destination contract, but Ressik will not create an implicit full local
mirror for a cloud-backed plan.

The logical-to-physical indirection contains the experimental pack complexity.
Ressik can ship standalone-only storage before publishing any pack format. Once
packs have been published, disabling new packing still requires existing packs
to remain readable or to be drained through the compactor.

## Alternatives considered

- A complete local mirror provides simple offline backup and restore, but
  requires approximately the protected data size in additional local storage.
- One complete SQLite repository checkpoint per backup simplifies cache
  recovery, but reuploads all retained history and cannot be produced from a
  bounded cache after history has been evicted.
- A local SQLite cache with JSON manifests as the remote authority avoids a
  SQLite dependency in the format, but duplicates the logical schema and makes
  cache rebuild, querying, and repair follow separate code paths.
- Mutable packs that reuse dead byte ranges avoid copying live members, but
  object stores replace whole objects rather than modifying completed ranges.
  Overwriting a pack in place would also make crashes destructive.
- One remote object per logical block makes deletion simple, but performs poorly
  for repositories containing many small files.
- Exact Arq-format compatibility would replace Ressik's BLAKE3 identifiers,
  AES-GCM framing, and Merkle encoding while leaving multi-destination
  reconciliation as custom work.

## Non-goals

- Cross-repository or cross-user deduplication.
- Completing backups while every destination is unavailable and staging is
  full.
- Uploading `repository.key`, credentials, or plaintext catalog data.
- Choosing permanent pack-size, packing-threshold, grace-period, minimum-age,
  cache-cap, or catalog-cap defaults in this ADR.

## References

- [ADR 0002: Use a minimal S3-compatible client](0002-use-a-minimal-s3-client.md)
- [ADR 0003: Define a repository transfer protocol](0003-define-repository-transfer-protocol.md)
- [Repository format version 1](../repository-format.md)
- [SQLite Online Backup API](https://www.sqlite.org/backup.html)
- [Arq 7 data format](https://www.arqbackup.com/documentation/arq7/English.lproj/dataFormat.html)
