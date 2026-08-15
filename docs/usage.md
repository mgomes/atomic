# Using Atomic

This guide covers day-to-day operation after a repository and at least one
backup plan have been configured. See the [configuration guide](configuration.md)
for setup details.

## Run and inspect backups

Run a plan, inspect its snapshots, or force a full source scan:

```sh
./atomic run documents
./atomic snapshots documents
./atomic run --full documents
```

After the first snapshot, Atomic uses the previous Merkle manifest as its scan
index. A regular file whose path, type, size, mode, modification time,
filesystem identity, and change time are unchanged reuses its authenticated
block references without rereading file contents. Changed files are read and
hashed normally.

A full run rereads and hashes every source file. Run one periodically to cover
filesystems whose change metadata is coarse or unavailable.

## Restore a snapshot

```sh
./atomic restore SNAPSHOT_ID --to ./restored
```

The restore destination must not already exist, and its parent directory must
exist. Atomic builds the complete restore in a private sibling directory and
publishes it with one rename, so a failed restore never leaves a half-populated
destination. Publication is atomic and refuses to replace a destination
created during the restore.

The destination gets one top-level file, directory, or link per configured
source ID. For example, the `documents` source restores beneath
`./restored/documents`.

## Verify and clean the repository

`atomic verify` authenticates every encrypted manifest and unique block and
recomputes every Merkle root:

```sh
./atomic verify
```

This is intentionally slower than a regular incremental backup, which only
stats unchanged block objects.

The daemon removes interrupted-write leftovers when it starts. CLI-only users
can remove unreachable encrypted blocks, manifests, and temporary objects
after a hard crash:

```sh
./atomic gc
```

## Terminal dashboard

Running `atomic` without a command opens the dashboard. Scripts and launchers
can invoke it explicitly:

```sh
./atomic tui
```

Use arrow keys or `j`/`k` to choose a plan, `r` to start it, `R` to
reload, and `q` to quit.
