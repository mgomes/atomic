# ADR 0001: Store unattended credentials in protected files

Status: Accepted

Date: 2026-07-11

## Decision

Atomic will use per-user protected files as the primary store for destination
credentials rather than embedding them in `config.yaml`, repository metadata,
or daemon state. Each configuration path gets a separate credential directory,
and each credential ID names one opaque, provider-owned file within it.

On POSIX systems, Atomic requires the directory to be owned by the effective
user with exact mode `0700`. Credential files must be regular files owned by
that user with exact mode `0600` and at most one live hard link. Atomic rejects
symbolic links and insecure existing paths before reading bytes. It does not
silently tighten an existing path because that cannot undo an earlier exposure.

Windows does not implement POSIX modes as an access-control boundary. Atomic
therefore creates the directory and files with a protected DACL at creation
time. The current user must own the object and be the only trustee, with full
control. Reparse points and additional access rules are rejected. Filesystems
that cannot enforce this contract are unsupported for credential storage.

Creation and rotation use a protected temporary file in the credential
directory, followed by an atomic rename. Creation refuses to replace an
existing credential; rotation requires an existing protected credential.

Static S3, Backblaze B2, and Cloudflare R2 credentials fit directly in this
store. A future Google Drive authorization flow will store its long-lived
refresh token here and keep short-lived access tokens in memory. That flow will
use an installed-application OAuth authorization and must surface revocation or
expiration as a reauthorization-required error.

## Context

Atomic is designed to run unattended after login or reboot. A credential vault
that requires a manual unlock can silently prevent scheduled backups until a
user notices. Putting credentials directly in YAML makes them too easy to copy,
log, or commit and gives unrelated configuration tooling access to secrets.

Google Drive differs from the object-storage providers because it uses OAuth,
but it still produces a refresh token intended for secure long-term storage and
capable of refreshing access without repeated user interaction.

## Consequences

Scheduled backups can start without an unlock prompt, and provider adapters can
share one small persistence boundary without sharing credential schemas. A
future keychain or vault backend can sit behind the same provider-facing API.

The files protect against other local accounts, accidental disclosure, and
overly broad inherited permissions. They do not protect against the owning
account, an administrator or root compromise, malware running as that user, or
an attacker who can read process memory. Credential files need an intentional
recovery plan. If a configured source contains Atomic's application-data tree,
the credential files are ordinary source files and will be included in the
encrypted snapshot unless a global ignore rule excludes them.

Exact ACL enforcement means the daemon must run as the account that created the
credentials. Moving to a system-wide service would require an explicit
provisioning and trustee model rather than broadening access implicitly.

## Alternatives considered

- System keychains avoid plaintext files but differ substantially across
  platforms and can be unavailable to a background process while locked.
- A passphrase-encrypted vault has the same unattended-unlock problem unless
  its passphrase is stored somewhere equivalent to this credential file.
- Environment variables move secret management to a supervisor and are easy to
  expose through process configuration or diagnostic output.
- Credentials in `config.yaml` are simple but unnecessarily increase their
  disclosure surface.

## References

- [OAuth 2.0 for native apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Google OAuth token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)
