# fixture app

The Android app forja's e2e suite uses. It is built, installed, and driven
from `e2e/`.

> **This app is the e2e suite's contract (fixture).**
> Breaking the button IDs (`fetch_singleton` / `fetch_new`), the
> `MainActivity` FQN (`com.tkhskt.forja.sample.MainActivity`), or the two
> flavors (`devDebug` / `stagingDebug`) will break the e2e tests.

## UI layout

`MainActivity` exposes two buttons so verification scenarios A and B can be
driven independently.

| Button | Client used | Path it verifies |
|---|---|---|
| **A: Fetch via SINGLETON client (Http.client)** | `Http.client` (eagerly built in `Application.onCreate`) | Whether agent.cpp's `IterateOverInstancesOfClass` path **rewrites already-built instances** |
| **B: Fetch via NEW client each time (Builder.build)** | A fresh `OkHttpClient.Builder().build()` per click | Whether agent.cpp's **Breakpoint + per-thread MethodExit** catches `build()` calls made after attach |

Both buttons hit `https://example.com/` (IANA-reserved domain, very stable
response).

## Two flavors

From `productFlavors` in `build.gradle.kts`:

- `devDebug` → `com.tkhskt.forja.sample`
- `stagingDebug` → `com.tkhskt.forja.sample.staging` (`applicationIdSuffix = ".staging"`)

Used by the e2e suite to set up the "multiple debuggable packages
coexisting" scenario.

## Driving the app standalone

```bash
cd e2e/fixtures/app
./gradlew installDevDebug   # for staging: installStagingDebug
adb shell am start -n com.tkhskt.forja.sample/com.tkhskt.forja.sample.MainActivity
```

Push a rule with forja:

```bash
./bin/forja rules add mock-teapot \
  --pkg com.tkhskt.forja.sample \
  --host example.com --path / \
  --status 418 --body '{"rewritten":true}'
```

## Capability-dependent scenarios

On devices where ART denies the agent
`can_generate_breakpoint_events` / `can_generate_method_exit_events`, the
agent logs at attach time:

```
ForjaAgent  W  capabilities: tag_objects=YES, breakpoint=NO, method_exit=NO
                       — new clients will NOT be picked up (existing-only mode)
```

In that case **button B keeps returning HTTP 200** (the rewrite isn't
applied to new clients), while button A still returns HTTP 418 as expected.
