# Development

The `justfile` exposes the standard development checks:

```sh
just check
just race
just test-full
just build
just cross
```

`just test-full` runs the backup package with a slower,
production-block-size lifecycle test enabled. It backs up and mutates a
multi-generation corpus, checks deduplication, ignores, retention, and garbage
collection, restores complete historical trees, and proves that corrupted
encrypted blocks cannot be verified or restored.

The full lifecycle test is excluded from pull-request CI and runs nightly on
Linux, macOS, and Windows. The
[Full lifecycle workflow](../.github/workflows/full.yml) can also be started
manually.
