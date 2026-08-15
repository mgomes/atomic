# Background service

Atomic can run scheduled plans in the foreground or install itself through the
platform's per-user background manager.

## Run in the foreground

```sh
./atomic daemon
```

Foreground mode is useful with a container, process supervisor, or terminal
while diagnosing schedules.

## Install and manage the service

```sh
./atomic service install
./atomic service status
./atomic service restart
./atomic service uninstall
```

Installation starts the job unless `--no-start` is supplied. Uninstalling
never deletes configuration, repository keys, snapshots, or daemon state.

The installed job records absolute configuration and state paths. Keep the
Atomic executable at the path from which it was installed. When installing
with `--config` or `--state-dir`, repeat the same selectors on every later
`service` command so it addresses the same job.

## Schedule status and retries

`atomic status` shows ten schedule slots per plan, from oldest to newest,
without opening the encrypted repository or inspecting source paths. A green
`●` succeeded, a red `×` failed, and a gray `·` has no durable result.
Gray includes a missed occurrence, a backup interrupted before its result was
recorded, and history from before this status format existed. Retries update
their original schedule slot instead of adding another mark.

A failed scheduled occurrence remains pending and retries with persisted
exponential backoff, starting at one minute and capped at one hour. A restart
does not reset that backoff or silently consume the missed occurrence.

## macOS and Linux

On macOS, Atomic installs a per-user LaunchAgent. On systemd-based Linux it
installs a user unit. Neither normally requires administrator access.

Other Linux init systems can supervise `atomic daemon` directly but are not
handled by `atomic service` yet. A systemd user unit stops after logout unless
lingering is enabled by the administrator:

```sh
loginctl enable-linger "$USER"
```

## Windows

Windows installs a per-user Task Scheduler job with an interactive token and
the least-privilege run level. Run installation from the normal, non-elevated
shell for the account that owns the backups.

No password is stored, and the task can run only while that user is signed in.
While enabled, it starts again at the next logon. `service stop` disables
future logon starts until `service start` or `service restart` enables the
task again. This avoids giving a user-writable backup configuration to
LocalSystem or another privileged service account.

Windows task activity and fatal startup errors are appended to `daemon.log`
in that config instance's state directory. Stop, restart, and uninstall first
request cooperative cancellation; after 25 seconds, Task Scheduler may
terminate a stuck process, relying on the same crash-recovery rules as a power
interruption.

## Multiple configurations

Default scheduler state is namespaced by the absolute, symlink-resolved config
path, so symlink aliases share one daemon lock and schedule history while
distinct configs remain independent. Use consistent path spelling on
case-insensitive filesystems; hard-link aliases are distinct selectors.
