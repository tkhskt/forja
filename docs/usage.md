# Usage

forja treats the current working directory as a single "project". The yml file is **the rule catalog** and `status.json` is **the per-app on/off state** — those two responsibilities live in separate places: the catalog is authored under `.forja/`, while the state is machine-managed in the user cache.

| File | Scope | Git | Role | Edit by hand? |
|---|---|---|---|---|
| `.forja/rules.yml` | **project** | commit | rule catalog shared by the team | ✅ |
| `.forja/rules.local.yml` | **local** | gitignore | personal rule catalog (overrides project) | ✅ |
| `.forja/aliases.yml` | **project** | commit | short-name map for `--app`, shared by the team | ✅ |
| `.forja/aliases.local.yml` | **local** | gitignore | personal short-name map (overrides project) | ✅ |
| `<cache>/forja/status/<project>.json` | (state) | — (outside repo) | **per-app** enabled state | ❌ CLI-managed |

`.forja/` holds only authored content. The per-app enabled state (`status.json`) is machine-managed transient state, so it lives in the OS user cache (`~/Library/Caches/forja/status/` on macOS, `$XDG_CACHE_HOME/forja/status/` or `~/.cache/forja/status/` on Linux), keyed by the project's root path. It's never committed and there's nothing to gitignore for it. A pre-existing `.forja/status.json` from an older forja is migrated into the cache automatically on the next command.

The yml file holds no information about which app a rule targets, so the same rule can be reused across multiple apps (dev/staging variants, multiple apps in a monorepo, etc.).

Beyond the two root files, **rules can be split into bundle directories**: any `rules.yml` / `rules.local.yml` found under `.forja/` (e.g. `.forja/rules/payments/rules.yml`) is discovered and merged. A bundle is a self-contained, shareable unit. See [Rule handles & bundles](#rule-handles--bundles).

forja never creates the `.forja/` directory on its own — `forja init` is the one-time setup step. Every other command refuses to run if `.forja/` is missing from the current cwd, so accidentally invoking forja from the wrong directory can't silently spawn an orphan `.forja/` somewhere unexpected.

---

## One-time setup: `forja init`

Run once at the project root before any other command.

```bash
forja init
```

The command:
- creates `.forja/` and seeds `.forja/rules.yml` with a schema-commented template,
- prints the recommended `.gitignore` entries so you can add them by hand (init does not edit `.gitignore` itself — VCS hygiene is your call, matching the convention of ESLint / Prettier / terraform / tsc).

`init` refuses to overwrite an existing `.forja/rules.yml` so a populated catalog can never be silently wiped by a stray re-run. Re-init in a fresh checkout is safe.

---

## Workflow examples

### 1. Add a rule, then apply it to an app

```bash
forja rules add slow-bar --host example.com --path /bar --status 503
forja apply --app com.tkhskt.sample_app --enable slow-bar
```

`rules add` writes to `.forja/rules.yml` (the project / committed catalog) by default. Pass `--local` to write to `.forja/rules.local.yml` instead — that file is meant to be gitignored, for personal overrides on top of the team-shared catalog. Pass `--dir <path>` to write into a bundle (`.forja/<path>/rules.yml`) — see [Rule handles & bundles](#rule-handles--bundles).

`apply` is what actually flips `status.json` and pushes the new effective ruleset to the device.

### 2. Iterate

```bash
forja rules update mock-failure --status 502
```

Patch semantics — only the fields you pass change. Auto-pushes to every app where the rule is currently enabled.

### 3. Hand-edit the yml, then sync

```bash
$EDITOR .forja/rules.yml
forja sync                 # re-push to every app with a status entry
forja sync --app dev       # or just one app (alias or full name)
```

`sync` is read-only on `status.json` — it never changes which rules are enabled, only re-pushes the current effective set so a hand edit reaches the device.

### 4. Toggle interactively (TUI)

```bash
forja rules                           # picks an app, then opens the toggle list
forja rules --app com.tkhskt.sample_app   # skip the picker
```

Pressing `q` saves and pushes. Pressing `ctrl-c` discards changes.

### 5. Clear an app

```bash
forja off --app com.tkhskt.sample_app
```

Turns off every rewrite on the named app, so the app sees the original (real) responses again. Other apps are untouched. Re-enable rules anytime via `forja apply` or the TUI.

---

## Command summary

| Command | Behavior |
|---|---|
| `forja init` | One-time setup: create `.forja/rules.yml` with a schema-commented template, and print the recommended `.gitignore` entries (does not edit `.gitignore` itself) |
| `forja rules add NAME [flags]` | Append a rule to the yml catalog. Does NOT push to any device — use `forja apply` or the TUI next |
| `forja rules update NAME [flags]` | Patch the yml + **auto-push to every app where the rule is enabled**. `--no-sync` suppresses the push |
| `forja rules remove NAME` | Delete from yml + **auto-push to every app where it was enabled** + drop the entry from every app in `status.json`. `--no-sync` suppresses the push |
| `forja rules list [--app X]` | Print the catalog (no device side effects). With `--app`, each row is prefixed `[on]` / `[off]` per `status.json` |
| `forja apply --app X --enable a,b [--disable c]` | Patch `status.json[X]` and push (one of `--enable`/`--disable` is required) |
| `forja sync [--app X]` | **Read-only on `status.json`.** Re-push the current effective rule set to every app with a status entry (or just X). Use this after hand-editing the yml to make the change visible on the device |
| `forja rules` | TUI: (1) list debuggable apps on the device → (2) pick one → (3) show the rule list with per-app toggles → q to push |
| `forja rules --app X` | TUI: skip the picker and jump straight to the rule list for X |
| `forja off --app X` | Turn off every rewrite on X (other apps untouched) |
| `forja alias set NAME APP_ID [--local]` | Register a short name to use anywhere `--app` is accepted. Defaults to **project** scope (`.forja/aliases.yml`, committed); pass `--local` for the personal file (`.forja/aliases.local.yml`, gitignore it) |
| `forja alias rm NAME [--local]` | Delete an alias from the target scope (project by default, `--local` for the personal file) |
| `forja alias list` | List registered aliases, grouped by scope |

Aliases have the **same two scopes as rules**: project (`.forja/aliases.yml`, committed, shared by the team) and local (`.forja/aliases.local.yml`, gitignored, personal). `set` / `rm` default to project; pass `--local` for the personal file. The two are merged when resolving `--app`, with **local entries overriding project** ones of the same name.

Every command that accepts `--app` **takes either an alias or a full applicationId**. Unknown inputs pass through as literal applicationIds, so things still work when no alias is set:

```bash
forja alias set dev com.tkhskt.forja.sample           # project scope (shared, committed)
forja alias set dev com.tkhskt.forja.sample --local   # personal override on this machine
forja apply --app dev --enable teapot                 # "dev" resolves to com.tkhskt.forja.sample
forja apply --app com.acme.plugin --enable y          # unknown alias → treated as a literal applicationId
```

---

## Shared flags on `rules add / update`

CLI flags stay flat; forja distributes them into the yml's `match:` / `response:` groups when it writes the file.

```
--host       match: exact HTTP host           (→ match.host)
--path       match: substring of encoded path (→ match.path)
             a `*` makes it a glob — each `*` matches one path segment
             (any run of non-'/' chars), e.g. /users/*/posts
--status     response: HTTP status code       (→ response.status)
--body       response: inline body            (→ response.body — JSON-object-looking
             strings become bodyObject on the wire, everything else is sent raw.
             Pass --body '' to force the response body to be empty)
--body-file  response: external file path     (→ response.bodyFile)
             path is relative to the yml's directory, or absolute
             .json extension → sent as bodyObject; anything else → raw string
--header     response: KEY=VALUE header override, repeatable (→ response.headers.KEY)
             Content-Type also drives the body's MIME type on the device
             (default application/json; charset=utf-8). On `update`, passing
             --header replaces the entire header map; pass --header '' to clear
--local      target the local (personal, gitignored) rules file (.forja/rules.local.yml).
             Default is project scope (.forja/rules.yml — the team-shared catalog)
--dir        (add only) write into .forja/<dir>/rules.yml — a shareable bundle
             directory (created if absent, must stay inside .forja/) instead of
             the root catalog. See "Rule handles & bundles"
--no-sync    (update / remove only) suppress the auto-push
```

`--body` and `--body-file` are **mutually exclusive** (passing both is an error). When `update` sets one of them, the other is automatically cleared.

`update` has patch semantics: **only the fields you pass on the command line change**. Pass `--status` alone and the host / path / body / headers are preserved.

### Returning non-JSON content types

The on-device runtime defaults to `application/json; charset=utf-8` when no `Content-Type` header is set. Override it via `--header`:

```bash
forja rules add html-mock \
    --host example.com --path / --status 200 \
    --body '<h1>hi from forja</h1>' \
    --header 'Content-Type=text/html; charset=utf-8'
```

The same shape works for any MIME type — `text/plain`, `application/xml`, `image/svg+xml`, etc.

### Forcing an empty response body

`--body ''` is distinct from "no body override": it explicitly replaces the response body with an empty one. Handy for `204 No Content`-style mocks where the upstream would normally return a payload:

```bash
forja rules add empty-204 \
    --host example.com --path /resource \
    --status 204 --body ''
```

Omitting `--body` entirely leaves the original response body untouched.

---

## Rule handles & bundles

Rules don't have to live in just the two root files. **Any `rules.yml` / `rules.local.yml` in a subdirectory under `.forja/` is discovered and merged**, so you can split rules into self-contained *bundles* — typically one directory per feature/team — and share a bundle by copying its directory. Its `responses/` assets come along, because `bodyFile` paths resolve relative to the file that declared the rule.

```
.forja/
├── rules.yml                      # root catalog (project)
├── rules.local.yml                # root catalog (local)
└── rules/
    ├── payments/
    │   ├── rules.yml              # a bundle: one or more related rules
    │   └── responses/
    │       └── declined.json
    └── search/
        └── rules.yml
```

> One directory holds at most one `rules.yml` (+ an optional `rules.local.yml`); a `rules.yml` may contain multiple rules. To split finer, add more subdirectories.

Create a rule directly in a bundle with `--dir` (the directory is created if absent and must stay inside `.forja/`):

```bash
forja rules add declined --status 402 --body-file responses/declined.json --dir rules/payments
# → writes .forja/rules/payments/rules.yml
```

### Handles

Every rule is addressed by a **handle**:

- a rule in the root `.forja/rules.yml` → just its **name**, e.g. `mock-failure`
- a rule in a bundle → **`<bundle>/<name>`**, e.g. `rules/payments/declined`

Within a single file every name must be unique, but **the same name may repeat across different bundles**. Commands that reference a rule (`apply --enable` / `--disable`, `rules update`, `rules remove`) accept a **bare name when it's unambiguous**; when the same name exists in multiple bundles, qualify it with the full handle — forja lists the candidates if you don't:

```bash
forja apply --app dev --enable declined                  # OK when "declined" is unique
forja apply --app dev --enable rules/payments/declined   # qualify when ambiguous
forja rules update rules/payments/declined --status 503  # update/remove take handles too
```

`status.json` records the resolved handle. A root rule's handle equals its name, so status files written before bundles existed keep working with **no migration**. Rule names may not contain `/` (it's the handle separator).

> The same name in one bundle's `rules.yml` **and** its `rules.local.yml` collides on handle (both would be `<bundle>/<name>`) and is rejected — give one a different name. To keep a personal variant of a *root* team rule, either give it a distinct name or put it in a local bundle so it gets a different handle.

---

## Rule schema

### `.forja/rules.yml` / `.forja/rules.local.yml`

Same schema in both files. The yml holds **no applicationId field and no `enabled` field** — it's a pure rule catalog. Each rule is split into two nested groups: **`match:`** decides whether the rule fires, **`response:`** decides what gets sent back.

```yaml
rules:
  - name: mock-failure
    match:
      host: example.com
      path: /foo
    response:
      status: 500
      body: '{"message":"failure"}'    # JSON object → encoded as a string in the yml
  - name: slow-bar
    match:
      host: example.com
      path: /bar
    response:
      status: 200
      body: "plain string body"        # any other string → sent as-is
  - name: big-response
    match:
      host: example.com
      path: /heavy
    response:
      status: 200
      bodyFile: responses/heavy.json   # external file (relative to the yml's directory, or absolute)
  - name: html-mock
    match:
      host: example.com
      path: /
    response:
      status: 200
      body: "<h1>hi from forja</h1>"
      headers:
        Content-Type: "text/html; charset=utf-8"
  - name: empty-204
    match:
      host: example.com
      path: /resource
    response:
      status: 204
      body: ""                         # explicit empty body (distinct from omitting it)
```

Both `match:` and `response:` are optional. A rule with only `response:` matches every request; a rule with only `match:` is a no-op (and gets flagged by `rules add` if it would have nothing to do).

`response.body` is **always a string scalar in the yml**. To send a JSON object, write it as a JSON-encoded string (as in `mock-failure` above) or use `bodyFile:` for larger payloads. The earlier `body:\n  key: value` mapping form is not supported — the yml will fail to load with a hint pointing at the supported forms.

`response.body: ""` is **the explicit empty-body case** — the device replaces the matched response with an empty one. Omitting the `body:` key entirely is different: the original response body passes through unchanged. The same distinction holds on the CLI (`--body ''` vs not passing `--body`).

`response.bodyFile` is mutually exclusive with `response.body`. At push time the file is read and:
- `.json` extension → parsed as a JSON object → sent as `bodyObject`
- anything else → raw bytes → sent as a raw string

So `big-response` above reads `.forja/responses/heavy.json`. Handy when you don't want a large JSON blob or HTML template inlined in the yml.

`response.headers` is an **optional map of header overrides** applied on top of the matched response. The `Content-Type` entry also drives the response body's MIME type on the device — by default the runtime returns `application/json; charset=utf-8`, so set `Content-Type` explicitly when returning HTML / plain text / XML / SVG / etc.

> **Header overrides are per-key, not wholesale.** Only the header names you list under `headers:` are replaced on the matched response; every other original header (`Date`, `Server`, `Cache-Control`, ...) passes through untouched. This matches the modify-existing-response model of Charles / mitmproxy. There is no current opt-in for "drop everything and use only my headers".

### Fields

`match:`

| Field | Purpose |
|---|---|
| `host` | Exact host match |
| `path` | Substring of encoded path; a `*` makes it a glob (each `*` matches one path segment, i.e. any run of non-`/` chars), e.g. `/users/*/posts` |

`response:`

| Field | Purpose |
|---|---|
| `status` | Replacement HTTP status |
| `body` | Inline body as a string scalar (write JSON objects as JSON-encoded strings: `'{"k":"v"}'`). `""` forces an empty body — distinct from omitting the key, which leaves the original body untouched |
| `bodyFile` | Read body content from an external file (mutually exclusive with `body`) |
| `headers` | Map of response header overrides. `Content-Type` here also sets the body's MIME type (default `application/json; charset=utf-8`) |

Top-level:

| Field | Purpose |
|---|---|
| `name` | Identifier, unique within its file. The addressable **handle** is `<bundle>/<name>` (or just `<name>` at the root) — see [Rule handles & bundles](#rule-handles--bundles) |
| `match` | Match conditions group (see above) |
| `response` | Replacement response group (see above) |

Only **the first matching rule** in the array is applied (OkHttp interceptor semantics).

---

### `status.json` (in the user cache)

> **Location:** the OS user cache — `~/Library/Caches/forja/status/<project>.json` on macOS, `$XDG_CACHE_HOME/forja/status/<project>.json` (or `~/.cache/forja/status/<project>.json`) on Linux. It is **not** under `.forja/` — it's machine-managed transient state, keyed by the project's absolute root path so separate checkouts don't clobber each other. There's nothing to commit or gitignore. A `.forja/status.json` left over from an older forja is migrated here automatically on the next command.

> **Don't edit this file by hand.** It's a CLI-managed mirror of what's currently pushed to each device. Manual edits won't reach the device and may be overwritten on the next `forja` invocation. The commands that write it are `forja apply`, the `rules` TUI's save action, `forja off`, and `forja rules remove`.
>
> The same warning ships inside the file as a top-level `$comment` key (JSON Schema-style metadata) so anyone opening it in an editor sees it on line 1. Any `$`-prefixed key is silently dropped on load, so the convention is forward-compatible with additional metadata (`$schema` etc.) without forja ever interpreting those entries as applicationIds.

The **per-app enabled rule list**, keyed by rule **handle**: "if a rule's handle is in app X's enabled list, the rule is on; otherwise it's off." Root rules appear as a bare name; bundle rules appear as `<bundle>/<name>`. Two values, no middle ground:

```json
{
  "$comment": "THIS FILE IS GENERATED BY forja. DO NOT EDIT BY HAND.",
  "com.tkhskt.sample_app": {
    "enabled": ["mock-failure", "rules/payments/declined"]
  },
  "com.tkhskt.sample_app.staging": {
    "enabled": ["mock-failure"]
  }
}
```

`"enabled": []` (an empty array) means "forja has touched this app but nothing is enabled right now" — that's the state right after `forja off --app X`. If the app key itself is absent, forja has never interacted with that app.

---

## Recommended `.gitignore`

forja does not edit `.gitignore` for you. When sharing across a team, add these lines yourself:

```gitignore
# forja: don't commit personal rules / aliases
.forja/**/rules.local.yml
.forja/**/aliases.local.yml
```

These are the two personal-scope filenames forja recognizes, each with a `**` so bundle-level copies are caught too — a flat list of just the root files would silently miss `.forja/payments/rules.local.yml`. (Naming the files explicitly, rather than a blanket `*.local.yml`, also avoids sweeping up an unrelated file you happen to name `foo.local.yml`.)

If you'd rather keep all your personal rules in one place, remember that **any subdirectory is discovered as a bundle** — so you can make, say, a `.forja/local/` directory and gitignore it yourself. forja doesn't treat that path specially; it's just a bundle you choose not to commit, so it's up to you to add it to `.gitignore`.

(`status.json` is **not** listed — it lives in the user cache, outside the repo.)
