<h1 align="center">
  <img src="./atomic.svg" alt="Atomic" width="180">
</h1>

Atomic is a small, terminal-native backup program for encrypted, deduplicated
snapshots. It currently stores backups in a local repository and includes
scheduling, restore tooling, and integrity verification.

Atomic is experimental. Keep an independent backup until the repository format
and recovery workflow have had broader testing.

## Quick start

Build Atomic with Go 1.25 or newer:

```sh
go build -o atomic ./cmd/atomic
```

Create a configuration and local encrypted repository:

```sh
./atomic init
$EDITOR "$(./atomic config path)"
./atomic check
```

Add at least one backup plan to the generated YAML. The
[example configuration](atomic.example.yaml) shows a complete local setup.
Then create and inspect a snapshot:

```sh
./atomic run documents
./atomic snapshots documents
```

Verify or restore it when needed:

```sh
./atomic verify
./atomic restore SNAPSHOT_ID --to ./restored
```

Running `atomic` without a command opens the terminal dashboard.

## Documentation

- [Using Atomic](docs/usage.md) covers full backups, restores, integrity checks,
  cleanup, and the terminal dashboard.
- [Configuration](docs/configuration.md) documents paths, ignore rules,
  schedules, retention, and validation.
- [Background service](docs/background-service.md) covers scheduled jobs on
  macOS, Linux, and Windows.
- [Storage and security](docs/storage-and-security.md) explains encryption,
  repository keys, credential storage, and metadata exposure.
- [Repository format](docs/repository-format.md) defines the versioned object
  model and crash-safety rules.
- [Roadmap and current limitations](TODO.md) tracks unfinished capabilities and
  portability constraints.
- [Architecture decisions](docs/adr) record the reasoning behind major design
  choices.

## Development

Contributor commands and lifecycle-test details are in the
[development guide](docs/development.md).
