# How forja works (and how to extend it)

## Overview

When forja pushes to a device (via `forja apply`, the `rules` TUI's save action, `rules add --app X`, `forja sync`, or the auto-propagation of `rules update/remove`):

1. For each target app, check whether the app is running (`pidof <app>`)
2. If the PID differs from the cached one, the app was restarted, so re-attach the agent via `adb shell cmd activity attach-agent`
3. Merge `forja/rules.yml` + `rules.local.yml`, filter by `status.json[app].enabled`, convert to device JSON, and write it to `/data/data/<app>/files/rules.json`

The agent (`agent-bundle.dex`) at attach time:

- Enables `FileRulesProvider`'s **self-destruct mode**
- Walks every existing `OkHttpClient` instance via reflection and inserts the `RulesInterceptor`
- Sets a breakpoint on `OkHttpClient$Builder.build()` with per-thread MethodExit so new clients are caught too

Each time OkHttp calls `interceptor.rules()`:

- If `files/rules.json` exists → read it → cache in memory → **delete the file**
- Otherwise → return the in-memory cache

The net result:

- ✅ User kills the app → the agent goes away → the in-memory cache is gone → next launch is a clean slate
- ✅ Hammering `forja rules update` is idempotent (when the PID is unchanged, the attach is skipped and only `rules.json` is rewritten)
- ✅ Disk residue lives only for the tens of milliseconds between the push and the agent reading it

---

## Troubleshooting

| Symptom | Check |
|---|---|
| `app not running` error | Is the app actually launched? (`adb shell pidof <app>`) |
| Rewrite isn't applied after attach | `adb logcat -s ForjaAgent Forja` — look for `capabilities:` and `loaded N rule(s)` |
| `am attach-agent` failure | Is the app debuggable? On API 28+? Already running? |
| forja can't find the agent bundle | See the [bundle search order](install.md#bundle-search-order) |

---

## Module layout

```
cli/                     ... Go CLI (the `forja` binary)
  cmd/                   ... cobra command tree
  internal/
    config/              ... YAML I/O + device-JSON conversion
    adb/                 ... adb subprocess wrapper
    attach/              ... PID-baseline attach cache
    rules/               ... add/remove/toggle engine
    engine/              ... EnsureAttached + Push (CLI ↔ device orchestration)
    tui/                 ... bubbletea TUI models

runtime/                 ... on-device runtime (bundled into agent-bundle.dex)
  src/main/kotlin/com/tkhskt/forja/
    RulesInterceptor.kt
    FileRulesProvider.kt
    Rule.kt

jvmti-agent/             ... C++ JVMTI agent + Kotlin Bootstrap
  src/main/cpp/agent.cpp
  src/main/kotlin/com/tkhskt/forja/agent/Bootstrap.kt
```

---

## Tests

Unit tests for the Go CLI and the Kotlin runtime:

```bash
cd cli && go test ./...
./gradlew :runtime:test
```

End-to-end suite against an Android emulator + Maestro (covers attach / push / self-destruct / multi-app / OkHttp 4 + 5):

```bash
cd e2e
go test -tags e2e -v ./...
```

The suite installs the fixture app in three variants (`ok4Dev`, `ok4Staging`, `ok5Dev`) so it can exercise multi-app and multi-OkHttp scenarios. See [`e2e/README.md`](../e2e/README.md) for prerequisites (emulator, ADB, Maestro).

---

## Release procedure

Pushing a tag fires `.github/workflows/release.yml`:

1. **Create and push a tag**:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
2. The agent is built on ubuntu-latest (Android SDK + NDK setup → `./gradlew :jvmti-agent:bundleAgentDex`).
3. The forja binary is Go-cross-compiled for six targets (macOS arm64/amd64, Linux arm64/amd64, Windows arm64/amd64).
4. Agent + binary are packed into `forja_<version>_<os>_<arch>.{tar.gz,zip}` with SHA256 sums and attached to a GitHub Release.
5. The installer resolves the latest tag via the GitHub Releases API and downloads the matching tarball.

### Trying the workflow before tagging

`workflow_dispatch` lets you trigger the workflow manually (Actions tab → release → Run workflow). Passing `v0.1.0-test` (or similar) as `version` runs everything **without** creating a GitHub Release, leaving the artifacts attached to the run instead (= dry-run mode).

To verify pieces locally, [act](https://github.com/nektos/act) can help:

```bash
act workflow_dispatch -W .github/workflows/release.yml --input version=v0.1.0-test
```

The Android NDK install step is heavy inside a container, though, so triggering `workflow_dispatch` on GitHub is usually faster in practice.

---

## License check

Run `scripts/check-licenses.sh` whenever you add a dependency (it drives jk1's Gradle License Report plugin and `go-licenses`). It exits with 1 on any violation. Adjust the allowed list in [`scripts/allowed-licenses.json`](../scripts/allowed-licenses.json) when introducing a new compatible license intentionally.
