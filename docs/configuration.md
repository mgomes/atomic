# Configuration

Atomic uses one YAML configuration for repository location, global ignore
rules, backup plans, schedules, and retention. Start with
[`atomic.example.yaml`](../atomic.example.yaml) or generate an empty
configuration with `atomic init`.

## Location and path handling

`--config` has highest precedence, followed by `ATOMIC_CONFIG`, followed by
the platform user configuration directory:

| Platform | Default path |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/atomic/config.yaml`, normally `~/.config/atomic/config.yaml` |
| macOS | `~/Library/Application Support/atomic/config.yaml` |
| Windows | `%AppData%\atomic\config.yaml` |

Relative paths in YAML resolve from the symlink-resolved configuration file's
directory, never the process working directory. Atomic expands only a leading
`~` in repository and source path fields; those fields do not expand
environment variables, shell expressions, or globs. Quote native Windows paths
with single quotes when they contain backslashes.

## Ignore rules

The optional top-level `ignore` list applies to every plan and source;
per-plan ignore lists are intentionally unsupported. Rules match portable
source-relative paths and must use `/` separators on every platform.

A rule without `/` matches a basename at any depth, so `*.tmp`,
`.DS_Store`, and `node_modules` work throughout every source. A rule
containing `/` matches the complete path from each source root:
`build/*.map` is root-relative, while `build/**/*.map` also crosses nested
directories beneath `build`. The supported operators are `*`, `?`,
character classes such as `[0-9]`, and `**` as a whole path component.
Matching is case-sensitive, Unicode-normalized, and includes dotfiles.

When a directory matches, Atomic prunes it without reading its contents. The
explicitly configured source root is always captured, even if its filename
would match a rule. Ignore rules affect new snapshots only; changing them does
not alter or limit restores of older snapshots.

Negation, brace alternation, absolute patterns, traversal components, trailing
slashes, and native Windows separators are rejected. Creating or removing an
ignored direct child may still change its included parent directory's metadata;
ignoring the containing directory avoids churn from its descendants.

## Repository location and configuration identity

`atomic init` writes the local repository to the platform application-data
folder:

| Platform | Default repository |
| --- | --- |
| Linux | `$XDG_DATA_HOME/atomic/repository`, normally `~/.local/share/atomic/repository` |
| macOS | `~/Library/Application Support/atomic/repository` |
| Windows | `%LocalAppData%\atomic\repository` |

This keeps large backup data out of the Windows roaming profile. Omitting
`repository` from a hand-written configuration uses the same platform
default. An explicit value, including `./repository` for storage beside the
config, overrides it.

When `atomic init` is given an explicit `--config` path, it instead creates
a repository beside that config and records the absolute path in YAML. This
keeps multiple explicitly initialized configurations isolated from one
another.

`atomic init` also writes a random `configuration_id` that namespaces plan
history and retention inside a repository. Keep that value when moving the
config. A hand-written config may omit it; Atomic then derives a stable ID from
the config's canonical path. This prevents two configs that share a repository
and reuse a plan name from pruning one another's snapshots.

## Schedules and retention

Schedules support `daily` and `weekly`. Weekly schedules add a `days` list
containing `mon` through `sun`. Named IANA time zones work on Windows
because the binary embeds the Go time-zone database.

Retention rules use union semantics. With `keep_last: 14` and
`keep_for: 90d`, Atomic keeps at least 14 snapshots and every snapshot newer
than 90 days. A zero or omitted retention policy keeps everything.

## Strict validation

Unknown YAML fields, duplicate keys, aliases, anchors, merge keys, custom tags,
multiple documents, and unsupported destination configuration are rejected.
Destination providers are not implemented yet, so `destinations` must remain
empty. See the [roadmap](../TODO.md) for planned destination work.
