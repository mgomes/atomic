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
of 5% of the operation's protected-byte measurement: the current preflight for
backup and authenticated committed statistics for later snapshot operations.
Operations without such a measurement use no disk payload cache, and
configuration may lower but not raise the ceiling. Streaming and backpressure
handle transfers larger than the disk budget; a raisable ceiling would quietly
regrow the local mirror this decision removes. Ressik will reserve budget before
writing and leave a snapshot uncommitted rather than exceed the ceiling. Catalog
and rebuildable control metadata are bounded separately
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
pack-index, destination-record, replication-waiver, retention-removal,
control-checkpoint, and commit schemas, and distinct authenticated object
kinds. Version 1 keeps its existing meaning and remains readable; a reader must
never infer an object's schema from current configuration or reinterpret a
version 1 manifest as a version 2 catalog.

Version 2 deliberately reuses version 1 logical block IDs, block-key
derivation, and sealed block frames. A version 2 standalone block contains one
complete version 1 block frame, and a pack contains those frames verbatim. New
authenticated kinds apply only to version 2 catalogs, indexes, destination and
control records, and commits. Readers select the block decoder from the
authenticated frame version rather than the destination key. Changing block
framing or key derivation requires a later format version.

Migration is copy-and-verify, not an in-place rewrite. Ressik authenticates a
version 1 snapshot, writes its version 2 objects under the separate namespace,
and publishes the version 2 commit last. It leaves the version 1 snapshot intact
until every selected snapshot is committed and verified in version 2 and the
user explicitly removes the old copy. Migration copies authenticated version 1
block frames without resealing or changing their IDs.

This decision also amends ADR 0002's deliberately small client surface.
Version 2 adds ranged and version-specific GetObject; CreateMultipartUpload,
UploadPart, CompleteMultipartUpload, AbortMultipartUpload, and one page of
ListMultipartUploads; and one page of ListObjectVersions plus version-specific
DeleteObject. A read-only GetBucketLifecycleConfiguration operation supports
provider qualification. PutObject and CompleteMultipartUpload return the
provider version ID when one exists. Pagination remains above the client.

Multipart uploads use replayable bounded parts and sign each part's SHA-256
payload. A failed upload is aborted before returning. On startup, while holding
the repository writer lock, Ressik lists and aborts every incomplete multipart
upload beneath its repository prefix before publishing new objects. It restarts
an interrupted object rather than resuming parts, so ListParts is unnecessary.
For a provider with strong multipart listing, lifecycle cleanup is defense in
depth. A discovery-only provider must expose a verified lifecycle rule that
aborts incomplete uploads beneath the repository prefix; without it, the
adapter is restore-only. SigV4 streaming payloads, presigned URLs, bucket
mutation, ACLs, tagging, and a general AWS credential chain remain excluded.

## Snapshot publication

The backed-up SQLite catalog contains one snapshot's logical state. It excludes
credentials, retry timers, daemon status, destination acknowledgements, and
physical pack locations. Ressik will materialize the immutable catalog as a
standalone database through SQLite's Online Backup API rather than copying a
live database file and its journal. A build that enables SQLite's snapshot API
may use a snapshot handle to select the source read view, but that optional
handle does not replace the portable backup operation or become part of the
repository format.

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
catalogs.

Attaching a destination mints a random 128-bit destination ID. Ressik seals a
destination record under its dedicated version 2 authenticated kind containing
the repository ID, destination ID, and record version, then stores it beneath
that destination's repository prefix. Configuration stores the same ID, but a
remote copy is trusted only after the record authenticates with `repository.key`.

Recovery authenticates destination records at each configured location. It
accepts exactly one effective record or requires the user to select an ID
explicitly. Copying a destination also copies its identity and therefore does
not create another replica; attaching that copy as an independent obligation
requires minting and publishing a new destination record. Credentials and
display names are not part of destination identity, and waivers accept
historical IDs no longer present in configuration. A snapshot may be complete
on one destination while another is pending.

Ressik releases an individual staged object as soon as one destination durably
acknowledges the exact uploaded bytes. This object-level decision does not make
the snapshot visible: a destination publishes the commit only after it can
resolve and authenticate every referenced object. A local staged copy is not
retained merely because another required destination lags. After the first
snapshot commit, at least one physically complete destination remains pinned
until every required destination is physically complete or has an authenticated
waiver.

Removing a required destination is an explicit authenticated repository
operation. It publishes a replication-waiver record naming the snapshot and
destination to every remaining complete destination before clearing that pin.
Editing configuration or losing credentials never implies a waiver, and
Ressik refuses to remove a destination while it is the last physically
complete source for any snapshot with outstanding replication obligations. It
also refuses to remove the final current control authority while another
destination still owes an acknowledgement. Abandoning the data itself is a
separate, explicitly destructive snapshot deletion rather than a waiver.
Recovery applies only authenticated waiver and retention-removal records, so
an unavailable destination pins data until it catches up, the user deliberately
abandons delivery, or retention has removed the snapshot.

## Control state

Replication waivers and retention-removal records form an authenticated,
monotonic repository control log. Every record is immutable and sealed once.
Each removal record repeats the snapshot ID, exact commit and catalog digests,
and stable required-destination IDs from the commit, so its acknowledgement set
remains reconstructable after the commit is deleted.
Periodic authenticated checkpoints contain the cumulative effective records,
a strictly increasing generation, and the previous checkpoint digest. Two
different checkpoints at one generation, or a broken digest chain, are
corruption.

Control records are repository authority rather than rebuildable delivery
state. Ressik pins each record, or a checkpoint containing it, until every
non-waived destination required by the affected snapshot acknowledges that
control state. A record may be collected only after all such destinations
acknowledge a checkpoint containing it. The current checkpoint remains pinned
until a later cumulative checkpoint is acknowledged by the same set.
Configuration edits and ordinary garbage collection never discard the final
copy of current control state.

Recovery reconciles authenticated checkpoints and later control records before
admitting destination commits into the repository union. A returning
destination first receives and acknowledges current control state. Commits
named by an effective retention-removal record are ignored and scheduled for
ordered deletion even when their catalogs and payload remain valid.

After local state is lost, a recovered control head is current only when every
non-waived destination named by visible commits and control records either
presents a mutually consistent head or acknowledges that head. While any such
destination is unavailable, read-only restore may expose authenticated data as
control-unreconciled, but Ressik admits no stale commit into the writable union
and performs no backup commit, replication, waiver, retention, compaction, or
garbage collection.

Declaring a missing control authority permanently lost is an explicit
authenticated recovery operation that starts a new control epoch from the
selected head and records the abandoned destination IDs. It requires a
destructive warning. A destination later returning with an older or forked
epoch is rejected until the user explicitly reconciles it.

After applying control state, Ressik lists the remaining snapshot commits and
pack indexes and authenticates their catalogs. The union of valid commits is
reconstructed conservatively. The same snapshot ID with different catalog
digests is corruption: snapshot IDs are unique per capture, a catalog is sealed
exactly once and never resealed, and every destination receives byte-identical
catalog ciphertext, re-fetched from a complete destination when local staging
is gone. Physical locations are rebuilt and checked separately for each
destination; the presence of an authenticated commit alone does not prove that
destination is complete.

Each adapter declares whether its provider contract guarantees strongly
consistent read-after-write and list-after-write visibility. Strong listings
may establish absence during recovery and garbage collection. Without that
guarantee, listings are discovery only: Ressik may upload, replicate, restore
discovered snapshots, and permanently delete already-known versions, but it
never performs absence-based destructive garbage collection. Uncertain objects
remain stored. The provider check reports this limitation before enabling the
destination. AWS S3 and Cloudflare R2 use their documented strong consistency;
Backblaze B2 remains conservative unless its adapter can establish the same
guarantee. Immutable keys and version-specific deletion avoid B2's documented
same-key ordering limitation.

A discovery-only destination cannot be the sole recovered control authority
after local state loss because an omitted newer checkpoint is indistinguishable
from absence. It remains restore-only until the explicit authenticated
authority-recovery operation selects a head and starts a new epoch. Re-enabling
the destination abandons its prior epoch; every old-epoch record revealed later
is rejected even when a strongly consistent destination supplied the selected
head.

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

Published destination keys are immutable. Ressik verifies and reuses an
existing object instead of intentionally overwriting it; an ambiguous retry may
still create another byte-identical provider version. Every successful write
records its optional provider version ID in rebuildable physical-location
state.

A key-only DeleteObject is a visibility operation and is never evidence that
storage was reclaimed. On a strongly consistent destination, garbage collection
lists every version and delete marker for a retired key and permanently deletes
each by version ID. On an unversioned destination, where no version ID exists,
key-only deletion is sufficient.

A discovery-only versioned provider must expose a verified lifecycle rule that
permanently expires noncurrent versions and orphan delete markers beneath the
repository prefix. For every retired key, Ressik first sends a key-only delete
to create a visibility marker and make the current data version noncurrent. It
then deletes every discovered version immediately, while the lifecycle rule
bounds cleanup of versions omitted from listings and removes the orphan marker.
Without that rule, the adapter is restore-only. This requirement applies to
Backblaze B2, whose buckets are always versioned.

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

Garbage collection also applies a configured minimum object age as heuristic
defense against accidental overlap and recently abandoned uploads. Age alone
never makes an object deletable and need not exceed an unbounded publication.
The exclusive writer lock, authenticated root analysis, replication pins, and
physical-completeness checks are the correctness mechanisms. If age becomes a
safety boundary later, the protocol must define a finite renewable publication
lease and abort publication when that lease expires.

## Retention and compaction

Ressik evaluates retention before pending replication. A retained snapshot with
an incomplete, non-waived destination remains a replication root and pins at
least one physically complete source. An expired snapshot does not remain a
replication root merely because delivery is pending. Retention first publishes
an authenticated removal record that ends every outstanding replication
obligation for that snapshot.

Before deleting any snapshot object at a destination, Ressik durably publishes
the exact removal record there. Once the record exists on at least one durable
configured destination, replication of the expired snapshot stops. Each
reachable destination that acknowledges the record removes the snapshot commit
first, then its catalog, and only then payload unreachable from retained
commits. Offline destinations remain untouched until they return, reconcile
control state, and perform the same ordered deletion.

Removal completes only after every non-waived required destination acknowledges
the record or a cumulative checkpoint containing it. Until then, that control
state remains pinned even though snapshot payload need not remain at a
destination that has already acknowledged and deleted it. A crash before any
destination stores the record deletes nothing. A crash after storing it resumes
removal. A crash after deleting the commit leaves unreachable catalog or
payload for garbage collection. Losing every up-to-date control copy is loss of
repository authority; Ressik blocks mutation rather than guessing from stale
commits until the explicit authority-recovery operation selects a new epoch.

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

Payload accounting uses conservative allocated bytes rather than logical file
length alone. Ressik rounds each dense payload file's planned temporary and
final lengths up to the containing volume's allocation unit and counts all
simultaneous copies. It does not create sparse or reflinked payload files. If the
allocation unit cannot be determined safely, Ressik uses zero-disk payload
streaming instead of guessing.

The non-raisable payload-cache rule applies to every operation. Backup uses at
most 5% of the current preflight measurement. Restore uses at most 5% of the
selected snapshot's authenticated included-plaintext statistic. Verification
and catch-up replication process snapshots serially under each snapshot's
authenticated budget. Recovery, index rebuild, and standalone compaction have
no single protected-byte total and therefore use zero-disk payload streaming
through bounded memory. An optional absolute cache setting may lower these
limits but cannot raise them or replace them with a stale measurement.

When all destinations are unavailable, Ressik cannot both preserve an
arbitrarily large unfinished snapshot and obey the ceiling. It stops before the
next reservation and reports that destination delivery is blocking progress.
Compaction performed during a backup may share that run's reserved budget;
otherwise it streams without disk payload staging. It defers when neither path
can complete safely.

Catalog and rebuildable control state have a separate configured absolute byte
cap. This allowance may be raised when an active source tree requires more
metadata. The working database, WAL and shared-memory files, SQLite backup
output, encrypted catalog, atomic-write temporaries, delivery acknowledgements,
and physical-location rows reserve their worst-case simultaneous allocated
sizes against it. Rebuildable history is evicted before the active snapshot
fails.

Ressik reports payload and metadata usage and limits separately. Directory
entries and other filesystem bookkeeping are reported with the control
allowance, but their platform-dependent cost prevents a portable promise that
the volume's free-space delta exactly equals both limits. Metadata for empty or
tiny files can exceed 5% of their content bytes, so total Ressik disk use can
exceed 5% by the explicit metadata allowance; encrypted payload staging and
cache cannot.

## Consequences

Initial backups no longer require a second local copy of all protected data.
SQLite makes individual-file restore and logical reachability directly
queryable, while logical-to-physical indirection allows pack compaction without
rewriting snapshot metadata. A lost cache can be reconstructed from remote
commits, catalogs, and pack indexes.

The implementation now owes a SQLite integration that preserves
cross-compilation and behaves identically on every supported platform,
versioned schemas, consistent checkpoint creation, per-destination
reconciliation, range reads, authenticated pack indexes, copy-on-write
compaction, and failure-injection tests across every publication boundary.
The catalog also makes SQLite's stable, documented file format part of the
repository format. Cache rebuild may require listing and opening many pack
indexes. Compaction temporarily consumes additional remote
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
version 1 migration command before it can replace the current engine. Once
written, that specification is normative and this record keeps the rationale.
Version 1 read support remains until migrated snapshots have been verified
and deliberately removed; ending version 1 read support product-wide is a
separate later decision that this ADR does not schedule.

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
- Adopting restic's or kopia's pack and index conventions would reuse proven
  designs, but both assume their own chunking, key schedules, and repository
  layouts, and Ressik would inherit format decisions without gaining their
  tooling.
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
- [SQLite snapshot API](https://www.sqlite.org/c3ref/snapshot_get.html)
- [Arq 7 data format](https://www.arqbackup.com/documentation/arq7/English.lproj/dataFormat.html)
- [AWS deletion of versioned objects](https://docs.aws.amazon.com/AmazonS3/latest/userguide/DeletingObjectVersions.html)
- [AWS incomplete multipart cleanup](https://docs.aws.amazon.com/AmazonS3/latest/userguide/abort-mpu.html)
- [Amazon S3 consistency model](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html#ConsistencyModel)
- [Cloudflare R2 consistency model](https://developers.cloudflare.com/r2/reference/consistency/)
- [Backblaze B2 bucket versions](https://www.backblaze.com/docs/cloud-storage-s3-compatible-api-bucket-versions)
- [Backblaze B2 S3-compatible API](https://www.backblaze.com/docs/en/cloud-storage-call-the-s3-compatible-api)
- [Backblaze B2 lifecycle configuration](https://www.backblaze.com/apidocs/s3-get-lifecycle-configuration)
