# Ressik

Ressik is a small, terminal-native backup program. It stores encrypted,
deduplicated snapshots locally today; S3-compatible storage, Google Drive,
Backblaze B2, and Cloudflare R2 will be added as destination adapters later.

This is an experimental foundation. Keep an independent backup until the
repository format and recovery workflow have had broader testing.

## Quick start

Build Ressik with Go 1.25 or newer:

```sh
go build -o ressik ./cmd/ressik
```

Create an empty configuration and a local encrypted repository:

```sh
./ressik init
$EDITOR "$(./ressik config path)"
./ressik check
```

Add a plan to the generated YAML:

```yaml
version: 1

ignore:
  - ".DS_Store"
  - "*.tmp"
  - node_modules

plans:
  documents:
    name: Documents
    enabled: true
    sources:
      documents:
        path: ~/Documents
      pictures:
        path: ~/Pictures

    schedule:
      kind: daily
      at: "02:30"
      timezone: Local

    retention:
      keep_last: 14
      keep_for: 90d

destinations: {}
```

Then run, inspect, and restore snapshots:

```sh
./ressik run documents
./ressik run --full documents
./ressik status
./ressik snapshots documents
./ressik verify
./ressik restore SNAPSHOT_ID --to ./restored
```

The restore destination must not already exist, and its parent directory must
exist. Ressik builds the complete restore in a private sibling directory and
publishes it with one rename, so a failed restore never leaves a half-populated
destination. Publication is atomic and refuses to replace a destination
created during the restore. The destination gets one top-level file,
directory, or link per configured source ID; for example, the `documents`
source restores beneath `./restored/documents`.

The daemon removes interrupted-write leftovers when it starts. CLI-only users
can run `./ressik gc` after a hard crash to remove unreachable encrypted
blocks, manifests, and temporary objects.

`ressik verify` is the slower integrity path: it authenticates every encrypted
manifest and unique block and recomputes every Merkle root. Incremental backups
only stat unchanged block objects so their normal path remains fast. A
periodic `ressik run --full PLAN` rereads and hashes every source file as well,
covering filesystems whose change metadata is coarse or unavailable.

Running `ressik` with no command opens the dashboard. The TUI also has an
explicit command for scripts and launchers:

```sh
./ressik tui
```

Use arrow keys or `j`/`k` to choose a plan, `r` to start it, `R` to reload,
and `q` to quit.

## Configuration

`--config` has highest precedence, followed by `RESSIK_CONFIG`, followed by the
platform user configuration directory:

| Platform | Default path |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/ressik/config.yaml`, normally `~/.config/ressik/config.yaml` |
| macOS | `~/Library/Application Support/ressik/config.yaml` |
| Windows | `%AppData%\ressik\config.yaml` |

Relative paths in YAML resolve from the symlink-resolved configuration file's
directory, never the process working directory. Ressik expands only a leading
`~` in repository and source path fields; those fields do not expand
environment variables, shell expressions, or globs. Quote native Windows paths
with single quotes when they contain backslashes.

The optional top-level `ignore` list applies to every plan and source; per-plan
ignore lists are intentionally unsupported. Rules match portable
source-relative paths and must use `/` separators on every platform. A rule
without `/` matches a basename at any depth, so `*.tmp`, `.DS_Store`, and
`node_modules` work throughout every source. A rule containing `/` matches the
complete path from each source root: `build/*.map` is root-relative, while
`build/**/*.map` also crosses nested directories beneath `build`. The supported
operators are `*`, `?`, character classes such as `[0-9]`, and `**` as a whole
path component. Matching is case-sensitive, Unicode-normalized, and includes
dotfiles.

When a directory matches, Ressik prunes it without reading its contents. The
explicitly configured source root is always captured, even if its filename
would match a rule. Ignore rules affect new snapshots only; changing them does
not alter or limit restores of older snapshots. Negation, brace alternation,
absolute patterns, traversal components, trailing slashes, and native Windows
separators are rejected. Creating or removing an ignored direct child may
still change its included parent directory's metadata; ignoring the containing
directory avoids churn from its descendants.

`ressik init` writes a random `configuration_id` that namespaces plan history
and retention inside a repository. Keep that value when moving the config. A
hand-written config may omit it; Ressik then derives a stable ID from the
config's canonical path. This prevents two configs that share a repository and
reuse a plan name from pruning one another's snapshots.

`ressik init` writes the local repository to the platform application-data
folder: `~/.local/share/ressik/repository` on Linux,
`~/Library/Application Support/ressik/repository` on macOS, and
`%LocalAppData%\ressik\repository` on Windows. This keeps large backup data out
of the Windows roaming profile. Omitting `repository` from a hand-written
configuration uses this same platform default. An explicit value, including
`./repository` for storage beside the config, overrides it.

When `ressik init` is given an explicit `--config` path, it instead creates a
repository beside that config and records the absolute path in YAML. This keeps
multiple explicitly initialized configurations isolated from one another.

Schedules currently support `daily` and `weekly`. Weekly schedules add a
`days` list containing `mon` through `sun`. Named IANA time zones work on
Windows because the binary embeds the Go time-zone database.

Retention rules use union semantics. With `keep_last: 14` and `keep_for: 90d`,
Ressik keeps at least 14 snapshots and every snapshot newer than 90 days. A
zero or omitted retention policy keeps everything.

Unknown YAML fields, duplicate keys, aliases, anchors, merge keys, custom
tags, multiple documents, and unsupported destination configuration are
rejected.

## Background service

`ressik daemon` runs the scheduler in the foreground. This is useful with a
container, process supervisor, or a terminal while diagnosing schedules:

```sh
./ressik daemon
```

The same binary can install itself through the platform background manager:

```sh
./ressik service install
./ressik service status
./ressik service restart
./ressik service uninstall
```

`ressik status` shows ten schedule slots per plan, from oldest to newest,
without opening the encrypted repository or inspecting source paths. A green
`●` succeeded, a red `×` failed, and a gray `·` has no durable result. Gray
includes a missed occurrence, a backup interrupted before its result was
recorded, and history from before this status format existed. Retries update
their original schedule slot instead of adding another mark.

Installation starts the job unless `--no-start` is supplied. Uninstalling
never deletes configuration, repository keys, snapshots, or daemon state. The
installed job records absolute configuration and state paths. Keep the
Ressik executable at the path from which it was installed.
When installing with `--config` or `--state-dir`, repeat the same selectors on
every later `service` command so it addresses the same job.

A failed scheduled occurrence remains pending and retries with persisted
exponential backoff, starting at one minute and capped at one hour. A restart
does not reset that backoff or silently consume the missed occurrence.

On macOS, Ressik installs a per-user LaunchAgent. On systemd-based Linux it
installs a user unit. Neither normally requires administrator access. Other
Linux init systems can still supervise `ressik daemon` directly but are not
handled by `ressik service` yet. A systemd user unit stops after logout unless
lingering is enabled by the administrator:

```sh
loginctl enable-linger "$USER"
```

Windows installs a per-user Task Scheduler job with an interactive token and
the least-privilege run level. Run installation from the normal, non-elevated
shell for the account that owns the backups. No password is stored, and the
task can run only while that user is signed in; while enabled, it starts again
at the next logon. `service stop` disables future logon starts until
`service start` or `service restart` enables the task again. This avoids giving
a user-writable backup configuration to LocalSystem or another privileged
service account. Windows task activity and fatal startup errors are appended
to `daemon.log` in that config instance's state directory. Stop, restart, and
uninstall first request cooperative cancellation; after 25 seconds, Task
Scheduler may terminate a stuck process, relying on the same crash-recovery
rules as a power interruption.

Default scheduler state is namespaced by the absolute, symlink-resolved config
path, so symlink aliases share one daemon lock and schedule history while
distinct configs remain independent. Use consistent path spelling on
case-insensitive filesystems; hard-link aliases are distinct selectors.

## Storage and security

Ressik uses:

- BLAKE3 for Merkle trees and repository-scoped keyed block identifiers.
- Fixed 4 MiB blocks with repository-wide deduplication.
- AES-256-GCM with a random nonce and a separately derived key for every
  block, manifest, and commit marker.
- Encrypted manifests, so destinations do not receive plaintext filenames,
  paths, timestamps, or block membership.
- Commit-marker-last publication. POSIX durability prevents partial snapshots
  from becoming visible; Windows detects and reports marker/manifest
  inconsistencies after a best-effort power-loss boundary.

The 256-bit repository key is generated with the operating system CSPRNG and
stored as `repository.key` inside the local repository. Ressik never treats
that file as a destination object. Losing it makes every snapshot
unrecoverable, so copy it to a separate secure recovery location. On POSIX
systems Ressik requires that it have no group or other permissions; on Windows
its protection depends on the containing profile directory ACL.

Remote destination credentials will not be embedded in YAML, repository
metadata, or daemon state. Ressik's credential-store foundation puts them in a
directory namespaced by the canonical config path beneath the platform
application-data folder. On POSIX, the directory must be owned by the current
user with exact mode `0700`, and each credential must be a regular, owner-only
`0600` file. macOS extended ACLs are rejected. On Windows, Ressik enforces the
equivalent current-user-only protected DACL. Insecure files, symlinks or
reparse points, hard links, and broadened permissions are rejected rather than
repaired silently. Writes and credential rotations are synced and atomically
published.

| Platform | Credential directory |
| --- | --- |
| Linux | `$XDG_DATA_HOME/ressik/credentials/instances/<config-hash>`, normally `~/.local/share/ressik/credentials/instances/<config-hash>` |
| macOS | `~/Library/Application Support/ressik/credentials/instances/<config-hash>` |
| Windows | `%LocalAppData%\ressik\credentials\instances\<config-hash>` |

Google Drive will use the same store for its OAuth refresh token. Its eventual
setup command will still need one interactive browser authorization; normal
scheduled refreshes will not require an unlock prompt.

Credential paths are not implicitly excluded from source scans. If a source
contains Ressik's application-data tree, add an appropriate global ignore rule
unless you want the protected credential file included as ordinary encrypted
backup content.

Future remote destinations will still be able to observe ciphertext sizes,
upload timing, and reuse of an opaque block within one repository. Ressik does
not claim traffic-analysis resistance.

After the first snapshot, Ressik uses the previous Merkle manifest as its scan
index. A regular file whose path, type, size, mode, modification time,
filesystem identity, and change time are unchanged reuses its authenticated
block references without rereading file contents; changed files are read and
hashed normally. This makes the common incremental case fast. Filesystems with
coarse change metadata can still hide a same-size rewrite, which is why the
explicit full-source mode exists.

See [the repository format](docs/repository-format.md) for the versioned object
and crash-safety rules.

## Current limits

- Only the local encrypted repository is implemented. Cloud destinations are
  intentionally rejected by this build.
- Restore currently materializes every source in a snapshot; there is no
  individual-source or individual-path selector yet.
- Chunking is fixed-size. Content-defined chunking can be introduced as a new
  format version later.
- Backups are not filesystem-atomic. Ressik detects ordinary source mutations
  during capture and refuses to commit, but a filesystem snapshot remains the
  right source for databases and other constantly changing data.
- Symbolic-link contents are not traversed. Ressik does stat the target to
  record whether Windows must recreate a file or directory link.
- Version one preserves regular files, directories, their `rwx` permission
  bits and modification times on POSIX systems. Windows restore approximates
  permissions with its read-only attribute. Ressik also preserves portable
  relative symbolic-link targets.
  Absolute targets, backslashes, and non-portable target components are
  rejected so a restore never silently changes link meaning across operating
  systems. It does not preserve
  owners, ACLs, extended attributes, sparse extents, hard-link identity, or
  symlink metadata. Encountering a socket, FIFO, device, or another special
  file fails the plan instead of silently omitting it.
- Windows needs a known file-or-directory target kind to create a symbolic
  link, so a broken link captured on another operating system cannot be
  restored on Windows. Creating links also requires Developer Mode or the
  corresponding Windows privilege.
- Portable snapshot paths must be valid UTF-8 and cannot contain backslashes.
  Ressik rejects names that Windows could not restore faithfully.
- Background-manager code builds and runs unit tests on Windows, Linux, and
  macOS CI, but actual install/start/stop flows still need broader real-machine
  validation across manager versions.

## Development

```sh
just check
just race
just test-full
just build
just cross
```

`just test-full` runs the backup package with a slower, production-block-size
lifecycle test enabled. It backs up and mutates a multi-generation corpus,
checks deduplication, ignores, retention and garbage collection, restores
complete historical trees, and proves that corrupted encrypted blocks cannot
be verified or restored. The added test is excluded from pull-request CI and
runs nightly on Linux, macOS, and Windows; the workflow can also be started
manually.
