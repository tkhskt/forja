# e2e tests

End-to-end suite for forja. Fully automated: Go tests (`//go:build e2e`) +
Maestro flows + AVD.

Covers the things unit tests (`cd cli && go test ./...`) can't: real-device
attach, the self-destruct semantics, interceptor injection, and how forja
behaves when multiple debuggable apps coexist.

## Prerequisites

- macOS / Linux
- Go 1.25+
- Android SDK (`emulator` plus at least one installed system image; use
  `sdkmanager` to install a new one if needed)
- `$ANDROID_HOME` / `$ANDROID_SDK_ROOT` (auto-detected from
  `~/Library/Android/sdk` on macOS and `~/Android/Sdk` on Linux when unset)
- `avdmanager` / `sdkmanager` from either `cmdline-tools/latest/bin/` or the
  `tools/bin/` location
- `adb` (used from PATH if available, otherwise falls back to
  `$ANDROID_HOME/platform-tools/adb`)
- [Maestro](https://maestro.mobile.dev) (`curl -Ls "https://get.maestro.mobile.dev" | bash`)
- (Optional) An already-running emulator / device — detected and reused

## Run

Always go through the wrapper. It forces `-count=1` so Go's test cache never
short-circuits a real device run (a cached "PASS" from yesterday tells you
nothing about today's emulator state):

```bash
./e2e/scripts/run.sh
```

If an emulator is already running the suite runs in **borrow mode**
(`setup_emulator.sh` reuses the existing connection). Otherwise it creates
and boots an AVD (~3–5 minutes the first time).

The wrapper forwards every argument to `go test`, so common patterns stay
short:

```bash
# Keep the emulator alive after the run (useful for debugging)
FORJA_E2E_KEEP=1 ./e2e/scripts/run.sh

# Override AVD name / API level / ABI / image tag (e.g. to point at an
# already-installed system-image)
FORJA_E2E_AVD=my-avd \
FORJA_E2E_API=34 \
FORJA_E2E_ABI=arm64-v8a \
FORJA_E2E_TAG=google_apis_playstore \
./e2e/scripts/run.sh

# Run a single test
./e2e/scripts/run.sh -run TestCoreBasicRewrite
```

### Why not just `go test`?

`go test` caches results when source files don't change. That's correct for
pure unit tests but wrong for e2e — a cached PASS hides a real regression
when (say) the emulator OS was updated or an APK got reinstalled in between
runs. The wrapper passes `-count=1` to bypass the cache; using it
unconditionally avoids "I'm sure I ran the suite, why did CI catch this?"
moments.

## Suite layout

| File | Contents |
|---|---|
| `helpers_test.go` | Shared helpers: runForja / adbShell / waitForLogcat / readStatusJSON, etc. |
| `helpers_extra_test.go` | Fixture copying / inline Maestro flow |
| `core_test.go` | **The 5 core scenarios** (basic rewrite, self-destruct, kill, off, bodyFile) |
| `sync_test.go` | **`forja rules` sync-pattern tests** (add/update/remove auto-push, off, PID-change detection, project+local merge, etc.) |
| `multiapp_test.go` | **Multi-debuggable-app tests** (dev + staging coexisting, attach isolation, per-app off, per-app toggles of a shared rule) |

## TestMain lifecycle

1. Build the forja binary at `bin/forja`.
2. Build the agent bundle (`./gradlew :jvmti-agent:bundleAgentDex`).
3. Run `scripts/setup_emulator.sh` (detect an existing device or start an AVD).
4. Install both dev + staging flavors of the fixture app
   (`e2e/fixtures/app/`).
5. `m.Run()` executes each test.
6. Shut down the emulator (unless it was borrowed).

## The fixture app's two flavors

From `e2e/fixtures/app/build.gradle.kts`'s `productFlavors`:

- `devDebug` → `com.tkhskt.forja.sample`
- `stagingDebug` → `com.tkhskt.forja.sample.staging`

Both share the same source — `MainActivity` plus two buttons (A: singleton,
B: new client). `TestMain` installs both, so a multi-app debuggable
environment exists by default.

> The fixture app is e2e-only. If you want to fork it for manual
> experimentation, put a copy under `examples/` rather than mutating this
> one — the e2e contract (`fetch_singleton` / `fetch_new` button IDs, the
> `MainActivity` FQN, and the two-flavor split) must stay intact here.

## Adding a Maestro flow

When a new assertion pattern is needed, drop a YAML into `flows/`:

```yaml
appId: com.tkhskt.forja.sample
---
- launchApp:
    clearState: false
- tapOn:
    id: "fetch_singleton"
- assertVisible:
    text: "HTTP 418"
    timeout: 15000
```

Button IDs come from `android:id` in
`fixtures/app/src/main/res/layout/activity_main.xml`
(`fetch_singleton` / `fetch_new` / `output`).

For ad-hoc one-off assertions, use `runInlineMaestro(t, ...)` to pipe a raw
YAML string straight to maestro (see `helpers_extra_test.go`).

## Investigating failures

Emulator log: boot output lands at `${TMPDIR}/forja-e2e-emulator.log`.

Status file: `${TMPDIR}/forja-e2e-emulator.status` records whether the
emulator was started by us (`owned`) or borrowed (`borrow`) plus the PID.

logcat dumps are flushed via `t.Fatalf` on failure (this is what
`waitForLogcat` does).

To reproduce a failure under borrow mode:

```bash
adb logcat -s ForjaAgent Forja SampleApp    # watch in another terminal
FORJA_E2E_KEEP=1 go test -tags e2e -v -run TestCore... ./...
```

## Future CI integration

Currently local-only. A standard CI stack would combine:

- GitHub Actions: `reactivecircus/android-emulator-runner@v2` for the emulator
- Maestro setup: `maestro-mobile-dev/setup-maestro-action`
- Android SDK: `android-actions/setup-android@v4`

Out of scope for now.

## Coverage

- **core**: basic rewrite / self-destruct / process kill clearing state / off
  / bodyFile
- **sync**: auto-push for add/update/remove, off, PID-change detection,
  project + local merge
- **multiapp**: dev + staging operated simultaneously, attach isolation,
  per-app off, per-app toggles on a shared rule

TUI automation (the bubbletea / teatest layer) and parallel-device CI
support are intentionally out of scope.
