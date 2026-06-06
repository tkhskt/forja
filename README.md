# forja

A tool for rewriting OkHttp responses on Android apps **without rebuilding the app, without certificates, and without a proxy**.

- Targets debuggable APKs (= debug builds)
- The app's source code is untouched
- Rules disappear the moment the device process is killed (= session-scoped)
- Distributed as a single Go binary

## Requirements

- macOS / Linux (the CLI shells out to `adb`)
- Go 1.25+ (only when building from source)
- Android SDK + NDK (only when building the JVMTI agent from source)
- The target app uses OkHttp **4.x or 5.x** (both verified end-to-end —
  the fixture app has flavor dimensions for `okhttp:4.12.0` and
  `okhttp:5.3.0`, and the e2e suite exercises both)
- The target app is **debuggable** (debug builds are debuggable by default) and runs on API 28+
- `adb` is on `PATH` and a device or emulator is connected

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

- Installs the binary to `$HOME/.local/bin/forja` and the JVMTI agent (four ABI `.so` files plus `agent-bundle.dex`) to `$HOME/.local/share/forja/agent/`
- Supports macOS arm64 / amd64 and Linux arm64 / amd64
- No `sudo` required (override `PREFIX` for system-wide installs: `PREFIX=/usr/local curl ...`)

If `~/.local/bin` isn't on your `PATH` yet, the installer prints a hint:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Windows

The release page also ships `forja_<version>_windows_amd64.zip` and `forja_<version>_windows_arm64.zip`. The bash installer above does not run on native Windows — extract the archive manually:

1. Download the `.zip` from [Releases](https://github.com/tkhskt/forja/releases) and extract it (Explorer can do this natively).
2. Copy `bin/forja.exe` somewhere on your `PATH` (e.g. `%USERPROFILE%\.local\bin\forja.exe`).
3. Set `FORJA_BUNDLE_DIR` to the extracted `share/forja/agent/` directory, or move the agent files to `%USERPROFILE%\.local\share\forja\agent\` so forja can discover them automatically.

Windows users can also run the installer from WSL — it behaves identically to the Linux build there.

### Verify

```bash
forja --version
forja --help
```

Each `forja` command resolves the agent bundle directory in this order:

1. `--bundle DIR` flag
2. `$FORJA_BUNDLE_DIR`
3. `$XDG_DATA_HOME/forja/agent`
4. `$HOME/.local/share/forja/agent` ← installer default
5. `/usr/local/share/forja/agent`
6. `./jvmti-agent/build/outputs/agent` (fallback for in-repo development)

### Building from source (contributors / developers)

```bash
git clone https://github.com/tkhskt/forja
cd forja
./gradlew :jvmti-agent:bundleAgentDex    # JVMTI agent
(cd cli && go build -o ../bin/forja .)   # CLI binary
./bin/forja --help
```

When you build inside the repo, forja finds `./jvmti-agent/build/outputs/agent/` automatically — no environment variable required.

---

## Usage

forja treats the current working directory as a single "project". The yml file is **the rule catalog** and status.json is **the per-package on/off state** — those two responsibilities live in separate files.

| File | Scope | Git | Role | Edit by hand? |
|---|---|---|---|---|
| `forja/rules.yml` | **project** | commit | rule catalog shared by the team | ✅ |
| `forja/rules.local.yml` | **local** | gitignore | personal rule catalog (overrides project) | ✅ |
| `forja/status.json` | (state) | gitignore | **per-package** enabled state | ❌ CLI-managed |
| `forja/aliases.local.yml` | (personal) | gitignore | optional short-name map for `--pkg` | ✅ |

The yml file holds no information about which package a rule targets, so the same rule can be reused across multiple packages (dev/staging variants, multiple apps in a monorepo, etc.).

### Minimal workflow

```bash
# 1. Add a rule to the catalog (yml-only edit, nothing happens on the device)
./forja rules add mock-failure \
    --host example.com --path /foo \
    --status 500 --body '{"message":"failure"}'

# 2a. Pick a package and apply (= enable + push)
./forja apply --pkg com.tkhskt.sample_app --enable mock-failure

# 2b. Or use the sugar form on `add` to do it in one step
./forja rules add mock-failure --pkg com.tkhskt.sample_app \
    --host example.com --path /foo --status 500

# 3. Iterate on the value (patch + auto-push to every pkg where the rule is enabled)
./forja rules update mock-failure --status 503

# 4. TUI: pick package → toggle rules → q to save & push
./forja rules
./forja rules --pkg com.tkhskt.sample_app    # skip the picker
```

### Command summary

| Command | Behavior |
|---|---|
| `forja rules add NAME [flags]` | Add to the catalog (yml only). With `--pkg X`, also **enables on X and pushes** (sugar) |
| `forja rules update NAME [flags]` | Patch the yml + **auto-push to every pkg where the rule is enabled**. `--no-sync` suppresses the push |
| `forja rules remove NAME` | Delete from yml + **auto-push to every pkg where it was enabled** + drop the entry from every pkg in status.json. `--no-sync` suppresses the push |
| `forja apply --pkg X --enable a,b [--disable c]` | Patch `status.json[X]` and push (one of `--enable`/`--disable` is required) |
| `forja rules` | TUI: (1) list debuggable packages on the device → (2) pick one → (3) show the rule list with per-pkg toggles → q to push |
| `forja rules --pkg X` | TUI: skip the picker and jump straight to the rule list for X |
| `forja off --pkg X` | Empty X's `status.json` entry and push `[]` to the device (other packages untouched) |
| `forja alias set NAME PKG` | Register a short name to use anywhere `--pkg` is accepted (writes to `forja/aliases.local.yml` — a personal file you should gitignore) |
| `forja alias rm NAME` | Delete an alias |
| `forja alias list` | List registered aliases |

Every command that accepts `--pkg` **takes either an alias or a full package name**. Unknown inputs pass through as literal package names, so things still work when no alias is set:

```bash
forja alias set dev com.tkhskt.forja.sample
forja apply --pkg dev --enable teapot         # "dev" resolves to com.tkhskt.forja.sample
forja apply --pkg com.acme.plugin --enable y  # unknown alias → treated as a literal pkg
```

Shared flags on `forja rules add / update`:

```
--pkg        sugar: after editing yml, also enable on the given pkg and push
             (when omitted, only the yml is touched)
--host       match: exact HTTP host
--path       match: substring of encoded path
--status     rewrite: HTTP status code
--body       rewrite: inline body (JSON-object-looking strings become bodyObject,
             everything else is sent as a raw string)
--body-file  rewrite: read the body content from an external file
             (path is relative to the yml's directory, or absolute)
             .json extension → sent as bodyObject; anything else → sent as a raw string
--project    target the project scope (default is local)
--no-sync    (update / remove only) suppress the auto-push
```

`--body` and `--body-file` are **mutually exclusive** (passing both is an error). When `update` sets one of them, the other is automatically cleared.

`update` has patch semantics: **only the fields you pass on the command line change**. Pass `--status` alone and the host / path / body are preserved.

### Scope conflict resolution

When the same rule name appears in both project and local scopes (= shadow):

- **The local copy wins**
- The TUI shows only the local copy (the project copy is hidden)
- `forja rules update NAME` patches the local copy
- `forja rules update NAME --project` explicitly targets the project copy
- `forja rules remove NAME --project` deletes only the project copy (= the local shadow becomes visible)

---

## Rule schema

### `forja/rules.yml` / `forja/rules.local.yml`

Same schema in both files. The yml holds **no package field and no `enabled` field** — it's a pure rule catalog:

```yaml
rules:
  - name: mock-failure
    host: example.com
    path: /foo
    status: 500
    body: '{"message":"failure"}'    # JSON object → encoded as a string in the yml
  - name: slow-bar
    host: example.com
    path: /bar
    status: 200
    body: "plain string body"        # any other string → sent as-is
  - name: big-response
    host: example.com
    path: /heavy
    status: 200
    bodyFile: responses/heavy.json   # external file (relative to the yml's directory, or absolute)
```

The `body:` field is **always a string scalar in the yml**. To send a JSON object, write it as a JSON-encoded string (as in `mock-failure` above) or use `bodyFile:` for larger payloads. The earlier `body:\n  key: value` mapping form is no longer supported — the yml will fail to load with a hint pointing at the supported forms.

`bodyFile` is mutually exclusive with `body`. At push time the file is read and:
- `.json` extension → parsed as a JSON object → sent as `bodyObject`
- anything else → raw bytes → sent as a raw string

So `big-response` above reads `forja/responses/heavy.json`. Handy when you don't want a large JSON blob or HTML template inlined in the yml.

### `forja/status.json`

> **Don't edit this file by hand.** It's a CLI-managed mirror of what's currently pushed to each device. Manual edits won't reach the device and may be overwritten on the next `forja` invocation. The commands that write it are `forja apply`, the `rules` TUI's save action, `forja off`, `forja rules add --pkg X` (the sugar form), and `forja rules remove` (drops the rule from every pkg's enabled list).

The **per-package enabled rule list**: "if `name` is in pkg X's enabled list, the rule is on; otherwise it's off." Two values, no middle ground:

```json
{
  "com.tkhskt.sample_app": {
    "enabled": ["mock-failure", "big-response"]
  },
  "com.tkhskt.sample_app.staging": {
    "enabled": ["mock-failure"]
  }
}
```

`"enabled": []` (an empty array) means "forja has touched this pkg but nothing is enabled right now" — that's the state right after `forja off --pkg X`. If the pkg key itself is absent, forja has never interacted with that pkg.

### Recommended `.gitignore`

forja does not edit `.gitignore` for you. When sharing across a team, add these lines yourself:

```gitignore
# forja: don't commit personal rules / state / aliases
forja/rules.local.yml
forja/status.json
forja/aliases.local.yml
```

| Field | Purpose |
|---|---|
| `name` | Identifier (handle for add/remove, unique workspace-wide) |
| `host` | Exact host match |
| `path` | Substring of encoded path |
| `status` | Replacement HTTP status |
| `body` | Inline body as a string scalar (write JSON objects as JSON-encoded strings: `'{"k":"v"}'`) |
| `bodyFile` | Read body content from an external file (mutually exclusive with `body`) |

Only **the first matching rule** in the array is applied (OkHttp interceptor semantics).

---

## How it works (overview)

When forja pushes to a device (via `forja apply`, the `rules` TUI's save action, `rules add --pkg X`, or the auto-propagation of `rules update/remove`):

1. For each target pkg, check whether the app is running (`pidof <pkg>`)
2. If the PID differs from the cached one, the app was restarted, so re-attach the agent via `adb shell cmd activity attach-agent`
3. Merge `forja/rules.yml` + `rules.local.yml`, filter by `status.json[pkg].enabled`, convert to device JSON, and write it to `/data/data/<pkg>/files/rules.json`

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

## Troubleshooting starting points

| Symptom | Check |
|---|---|
| `app not running` error | Is the app actually launched? (`adb shell pidof <pkg>`) |
| Rewrite isn't applied after attach | `adb logcat -s ForjaAgent Forja` — look for `capabilities:` and `loaded N rule(s)` |
| `am attach-agent` failure | Is the app debuggable? On API 28+? Already running? |

---

## For developers

### Tests

```bash
cd cli && go test ./...
```

### Module layout

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

### Related docs

- [`e2e/README.md`](e2e/README.md) — the fully automated e2e suite (Go test + Maestro)

### Release procedure

Pushing a tag fires `.github/workflows/release.yml`:

1. **Create and push a tag**:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
2. The agent is built on ubuntu-latest (Android SDK + NDK setup → `./gradlew :jvmti-agent:bundleAgentDex`)
3. The forja binary is Go-cross-compiled for four platforms (macOS arm64/amd64, Linux arm64/amd64)
4. Agent + binary are packed into `forja_<version>_<os>_<arch>.tar.gz` with SHA256 sums and attached to a GitHub Release
5. The installer resolves the latest tag via the GitHub Releases API and downloads the matching tarball

### Trying the workflow before tagging

`workflow_dispatch` lets you trigger the workflow manually (Actions tab → release → Run workflow). Passing `v0.1.0-test` (or similar) as `version` runs everything **without** creating a GitHub Release, leaving the artifacts attached to the run instead (= dry-run mode).

To verify pieces locally, [act](https://github.com/nektos/act) can help:

```bash
act workflow_dispatch -W .github/workflows/release.yml --input version=v0.1.0-test
```

The Android NDK install step is heavy inside a container, though, so triggering `workflow_dispatch` on GitHub is usually faster in practice.

### License check

Run `scripts/check-licenses.sh` whenever you add a dependency (it drives jk1's Gradle License Report plugin and `go-licenses`). It exits with 1 on any violation. Adjust the allowed list in [`scripts/allowed-licenses.json`](scripts/allowed-licenses.json) when introducing a new compatible license intentionally.

---

## License

Distributed under the [Apache License 2.0](./LICENSE).

The only file with a different upstream is `jvmti-agent/src/main/cpp/jvmti.h`, which comes from OpenJDK under GPLv2 + the Classpath Exception. The Classpath Exception explicitly permits linking that file with code under any other license, so consumers of forja are not bound by GPLv2. See [`NOTICE`](./NOTICE) for details.
