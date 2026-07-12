# ADR 0003: Define a repository transfer protocol

Status: Accepted

Date: 2026-07-11

## Decision

Ressik will expose authenticated encrypted repository objects through typed
references and map them to provider-independent, format-versioned storage keys.
A stable 128-bit repository ID derived from `repository.key` with a dedicated
BLAKE3 derive-key context namespaces every destination object.

The canonical layout is `ressik/v1/<repository-id>/...`. Blocks retain their
two-character shard, manifests use the snapshot ID, and commit markers use the
same snapshot ID. Destination adapters must copy the exact stored ciphertext,
upload referenced blocks before the manifest, and publish the commit marker
last. The transfer methods never return local paths or `repository.key`.

## Context

The local repository stores immutable encrypted blocks, manifests, and commit
markers, but its path helpers and encryption codec are private implementation
details. Remote destinations need stable object names after a repository moves
or is restored on another machine. Multiple independently keyed repositories
may also share one bucket.

Commit markers authenticate the exact encrypted manifest. Resealing that
manifest would produce different ciphertext and invalidate the commit digest.
Copying local directory trees would also risk publishing the repository key,
locks, format metadata, and temporary files.

## Consequences

Destination adapters can copy authenticated objects without learning the local
layout or receiving the master key. Repository copies with the recovered key
converge on the same remote namespace, while unrelated repositories do not
deduplicate or collide. The versioned key prefix leaves room for a future
incompatible remote layout.

The public repository ID reveals that two destination namespaces belong to
copies of the same repository. Rotating `repository.key` changes the ID and
therefore requires a destination migration. Authenticated export decrypts each
object temporarily to verify it before returning the original ciphertext, so
replication pays one local authentication pass.

This decision does not provide durable delivery, retention pins, remote
garbage collection, or restore. Those layers must preserve commit-last
publication and must not treat transient daemon state as the only upload queue.
The replication layer must also keep the concrete `Repository` above the
provider boundary: adapters receive object keys and ciphertext or a
consumer-owned narrow interface, never a repository value that exposes
`Root()`.

## Alternatives considered

- A random ID stored in another repository metadata file would avoid deriving
  a public value from the key, but it creates another recovery-critical file
  that can become detached from `repository.key`.
- A user-configured prefix alone is easy to reuse accidentally and does not
  give restored copies a canonical namespace.
- Exposing repository paths would couple adapters to local format details and
  make accidentally copying `repository.key` possible.

## References

- [Repository format version 1](../repository-format.md)
- [ADR 0002: Use a minimal S3-compatible client](0002-use-a-minimal-s3-client.md)
