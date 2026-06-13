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

## Install / update

```bash
curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

Installs the binary to `$HOME/.local/bin/forja` and the JVMTI agent to `$HOME/.local/share/forja/agent/`. Supports macOS and Linux on arm64 / amd64.

**Re-run the same command later to update** — install.sh always fetches the current latest tag, wipes the agent dir, and recopies, so old `.so` files never linger across versions.

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

Rule definitions and per-app on/off state live in a `.forja/` directory at the root of your project. forja never creates that directory on its own — `forja init` is the one-time setup step.

### Basic workflow

```bash
# 0. One-time setup: create .forja/ + .forja/rules.yml with a schema-commented
#    template (also prints the recommended .gitignore entries to add by hand).
forja init

# 1. Add a rule to the catalog (yml only — nothing reaches the device yet)
forja rules add mock-failure \
    --host example.com --path /foo \
    --status 500 --body '{"message":"failure"}'

# 2. Open the TUI: pick an app from the device list,
#    toggle the rule on, q to save & push
forja rules
```

Non-interactive equivalent of step 2: `forja apply --app com.example.app --enable mock-failure`.

### Iterate on an enabled rule

```bash
# Patch any field — auto-pushes to every app where the rule is enabled
forja rules update mock-failure --status 502
```

### Hand-edit the yml, then sync to the device

```bash
$EDITOR .forja/rules.yml
forja sync
```

`sync` re-reads the yml and pushes to every app that already has rules enabled, without changing which rules are on.

See [the rule schema reference](docs/usage.md#rule-schema) for the full yml structure (`match:` / `response:` groups, `bodyFile:`, scope conflict resolution, etc.).

### Turn off all rewrites on an app

```bash
forja off --app com.tkhskt.sample_app
```

The app starts seeing the real responses again; the rule catalog (yml) stays intact, so you can re-enable later via the TUI or `forja apply`.

---

Rules are **session-scoped on the device**: kill the app and the rewrites disappear; relaunch and push again to get them back. Nothing is persisted in the app's filesystem long enough to survive a process restart.

## Docs

- [`docs/install.md`](docs/install.md) — full install reference (macOS / Linux / Windows / from source) + bundle resolution order
- [`docs/usage.md`](docs/usage.md) — complete command reference, rule schema (`.forja/rules.yml` + bundles, aliases), recommended `.gitignore`, scope conflict resolution
- [`docs/internals.md`](docs/internals.md) — how the JVMTI attach + interceptor injection works, troubleshooting, module layout, release procedure, license check

## License

Distributed under the [Apache License 2.0](./LICENSE).

Two third-party components are vendored under `jvmti-agent/src/main/cpp/`:

- **`slicer/`** — the dex bytecode rewriter from AOSP's [`tools/dexter`](https://android.googlesource.com/platform/tools/dexter), under the **Apache License 2.0** (the same license as forja). It's compiled into `libforja-agent.so` and used to instrument `okhttp3.OkHttpClient.interceptors()`.
- **`jvmti.h`** — from OpenJDK under **GPLv2 + the Classpath Exception**. The Classpath Exception explicitly permits linking that file with code under any other license, so consumers of forja are not bound by GPLv2.

See [`NOTICE`](./NOTICE) for the full third-party inventory.
