# forja

A tool for rewriting OkHttp responses on Android apps **without rebuilding the app, without certificates, and without a proxy**.

- Targets debuggable APKs (= debug builds)
- The app's source code is untouched
- Rules disappear the moment the device process is killed (= session-scoped)
- Distributed as a single Go binary

## Requirements

- macOS / Linux / Windows (the CLI shells out to `adb` / `adb.exe`)
- The target app uses OkHttp **4.x or 5.x** (both verified end-to-end)
- The target app is **debuggable** (debug builds are debuggable by default) and runs on API 28+
- `adb` is on `PATH` and a device or emulator is connected

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

Installs the binary to `$HOME/.local/bin/forja` and the JVMTI agent to `$HOME/.local/share/forja/agent/`. Supports macOS and Linux on arm64 / amd64.

For **Windows**, manual install, or **building from source**, see [`docs/install.md`](docs/install.md).

Verify:

```bash
forja --version
forja --help
```

If `~/.local/bin` isn't on your `PATH` yet:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Quick start

Rule definitions and per-package on/off state live in a `forja/` directory that forja maintains in your current working directory.

```bash
# 1. Add a rule and apply it to a package in one step
forja rules add mock-failure --pkg com.tkhskt.sample_app \
    --host example.com --path /foo \
    --status 500 --body '{"message":"failure"}'

# 2. Iterate — auto-pushes to every package where the rule is enabled
forja rules update mock-failure --status 502

# 3. Or hand-edit the yml directly, then push with `sync`
$EDITOR forja/rules.local.yml
forja sync

# 4. TUI — pick a package, toggle rules, q to save & push
forja rules

# 5. Turn off all rewrites on a package
forja off --pkg com.tkhskt.sample_app
```

Rules are **session-scoped on the device**: kill the app and the rewrites disappear; relaunch and push again to get them back. Nothing is persisted in the app's filesystem long enough to survive a process restart.

## Docs

- [`docs/install.md`](docs/install.md) — full install reference (macOS / Linux / Windows / from source) + bundle resolution order
- [`docs/usage.md`](docs/usage.md) — complete command reference, rule schema (`forja/rules.yml`, `status.json`, aliases), recommended `.gitignore`, scope conflict resolution
- [`docs/internals.md`](docs/internals.md) — how the JVMTI attach + interceptor injection works, troubleshooting, module layout, release procedure, license check

## License

Distributed under the [Apache License 2.0](./LICENSE).

The only file with a different upstream is `jvmti-agent/src/main/cpp/jvmti.h`, which comes from OpenJDK under GPLv2 + the Classpath Exception. The Classpath Exception explicitly permits linking that file with code under any other license, so consumers of forja are not bound by GPLv2. See [`NOTICE`](./NOTICE) for details.
