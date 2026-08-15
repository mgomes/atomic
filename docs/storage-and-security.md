# Storage and security

Atomic currently writes an encrypted local repository. The
[repository format](repository-format.md) defines its versioned objects and
crash-safety rules.

## Repository protection

Atomic uses:

- BLAKE3 for Merkle trees and repository-scoped keyed block identifiers.
- Fixed 4 MiB blocks with repository-wide deduplication.
- AES-256-GCM with a random nonce and a separately derived key for every
  block, manifest, and commit marker.
- Encrypted manifests, so repositories do not expose plaintext filenames,
  paths, timestamps, or block membership.
- Commit-marker-last publication. POSIX durability prevents partial snapshots
  from becoming visible; Windows detects and reports marker/manifest
  inconsistencies after a best-effort power-loss boundary.

## Repository key

The 256-bit repository key is generated with the operating system CSPRNG and
stored as `repository.key` inside the local repository. Atomic never treats
that file as a destination object.

Losing the key makes every snapshot unrecoverable, so copy it to a separate
secure recovery location. On POSIX systems Atomic requires that it have no
group or other permissions; on Windows its protection depends on the
containing profile directory ACL.

## Destination credentials

Remote destination credentials will not be embedded in YAML, repository
metadata, or daemon state. Atomic's credential-store foundation puts them in a
directory namespaced by the canonical config path beneath the platform
application-data folder:

| Platform | Credential directory |
| --- | --- |
| Linux | `$XDG_DATA_HOME/atomic/credentials/instances/<config-hash>`, normally `~/.local/share/atomic/credentials/instances/<config-hash>` |
| macOS | `~/Library/Application Support/atomic/credentials/instances/<config-hash>` |
| Windows | `%LocalAppData%\atomic\credentials\instances\<config-hash>` |

On POSIX, the directory must be owned by the current user with exact mode
`0700`, and each credential must be a regular, owner-only `0600` file.
macOS extended ACLs are rejected. On Windows, Atomic enforces the equivalent
current-user-only protected DACL. Insecure files, symlinks or reparse points,
hard links, and broadened permissions are rejected rather than repaired
silently. Writes and credential rotations are synced and atomically published.

Google Drive will use the same store for its OAuth refresh token. Its setup
command will still need one interactive browser authorization; normal
scheduled refreshes will not require an unlock prompt.

Credential paths are not implicitly excluded from source scans. If a source
contains Atomic's application-data tree, add an appropriate global ignore rule
unless you want the protected credential file included as ordinary encrypted
backup content.

See [ADR 0001](adr/0001-protected-credential-files.md) for the credential-store
threat model and design tradeoffs.

## Metadata exposure

Future remote destinations will still be able to observe ciphertext sizes,
upload timing, and reuse of an opaque block within one repository. Atomic does
not claim traffic-analysis resistance.
