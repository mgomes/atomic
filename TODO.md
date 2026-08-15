# Roadmap and current limitations

Atomic is still experimental. This file tracks user-visible work and constraints
that should remain easy to find without turning the README into a reference
manual.

## Planned capabilities

- [ ] Add S3-compatible destinations for AWS S3, Backblaze B2, and Cloudflare
  R2.
- [ ] Add Google Drive as a destination.
- [ ] Support restoring an individual source or path instead of materializing
  every source in a snapshot.
- [ ] Evaluate content-defined chunking as a new repository format version.
  The current format uses fixed-size chunks.
- [ ] Broaden real-machine validation of install, start, stop, and uninstall
  flows across supported background-manager versions. The manager code already
  builds and runs unit tests on Windows, Linux, and macOS CI.

## Backup consistency

Backups are not filesystem-atomic. Atomic detects ordinary source mutations
during capture and refuses to commit, but a filesystem snapshot remains the
right source for databases and other constantly changing data.

## Files and metadata

- Symbolic-link contents are not traversed. Atomic does stat the target to
  record whether Windows must recreate a file or directory link.
- Version one preserves regular files, directories, their `rwx` permission
  bits and modification times on POSIX systems. Windows restore approximates
  permissions with its read-only attribute.
- Atomic preserves portable relative symbolic-link targets. Absolute targets,
  backslashes, and non-portable target components are rejected so a restore
  never silently changes link meaning across operating systems.
- Owners, ACLs, extended attributes, sparse extents, hard-link identity, and
  symlink metadata are not preserved.
- Encountering a socket, FIFO, device, or another special file fails the plan
  instead of silently omitting it.

## Portability

- Windows needs a known file-or-directory target kind to create a symbolic
  link, so a broken link captured on another operating system cannot be
  restored on Windows. Creating links also requires Developer Mode or the
  corresponding Windows privilege.
- Portable snapshot paths must be valid UTF-8 and cannot contain backslashes.
  Atomic rejects names that Windows could not restore faithfully.
