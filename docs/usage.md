# Usage

forja treats the current working directory as a single "project". The yml file is **the rule catalog** and `status.json` is **the per-package on/off state** — those two responsibilities live in separate files.

| File | Scope | Git | Role | Edit by hand? |
|---|---|---|---|---|
| `forja/rules.yml` | **project** | commit | rule catalog shared by the team | ✅ |
| `forja/rules.local.yml` | **local** | gitignore | personal rule catalog (overrides project) | ✅ |
| `forja/status.json` | (state) | gitignore | **per-package** enabled state | ❌ CLI-managed |
| `forja/aliases.local.yml` | (personal) | gitignore | optional short-name map for `--pkg` | ✅ |

The yml file holds no information about which package a rule targets, so the same rule can be reused across multiple packages (dev/staging variants, multiple apps in a monorepo, etc.).

---

## Workflow examples

### 1. Add and apply in one step

```bash
forja rules add mock-failure --pkg com.tkhskt.sample_app \
    --host example.com --path /foo \
    --status 500 --body '{"message":"failure"}'
```

This appends the rule to `forja/rules.local.yml`, enables it on the named package in `status.json`, and pushes to the device.

### 2. Add to the catalog only, apply later

```bash
forja rules add slow-bar --host example.com --path /bar --status 503
forja apply --pkg com.tkhskt.sample_app --enable slow-bar
```

Useful when you want a shared catalog but per-developer enable choices.

### 3. Iterate

```bash
forja rules update mock-failure --status 502
```

Patch semantics — only the fields you pass change. Auto-pushes to every package where the rule is currently enabled.

### 4. Hand-edit the yml, then sync

```bash
$EDITOR forja/rules.local.yml
forja sync                 # re-push to every package with a status entry
forja sync --pkg dev       # or just one package (alias or full name)
```

`sync` is read-only on `status.json` — it never changes which rules are enabled, only re-pushes the current effective set so a hand edit reaches the device.

### 5. Toggle interactively (TUI)

```bash
forja rules                           # picks a package, then opens the toggle list
forja rules --pkg com.tkhskt.sample_app   # skip the picker
```

Pressing `q` saves and pushes. Pressing `ctrl-c` discards changes.

### 6. Clear a package

```bash
forja off --pkg com.tkhskt.sample_app
```

Turns off every rewrite on the named package, so the app sees the original (real) responses again. Other packages are untouched. Re-enable rules anytime via `forja apply` or the TUI.

---

## Command summary

| Command | Behavior |
|---|---|
| `forja rules add NAME [flags]` | Add to the catalog (yml only). With `--pkg X`, also **enables on X and pushes** (sugar) |
| `forja rules update NAME [flags]` | Patch the yml + **auto-push to every pkg where the rule is enabled**. `--no-sync` suppresses the push |
| `forja rules remove NAME` | Delete from yml + **auto-push to every pkg where it was enabled** + drop the entry from every pkg in `status.json`. `--no-sync` suppresses the push |
| `forja apply --pkg X --enable a,b [--disable c]` | Patch `status.json[X]` and push (one of `--enable`/`--disable` is required) |
| `forja sync [--pkg X]` | **Read-only on `status.json`.** Re-push the current effective rule set to every pkg with a status entry (or just X). Use this after hand-editing the yml to make the change visible on the device |
| `forja rules` | TUI: (1) list debuggable packages on the device → (2) pick one → (3) show the rule list with per-pkg toggles → q to push |
| `forja rules --pkg X` | TUI: skip the picker and jump straight to the rule list for X |
| `forja off --pkg X` | Turn off every rewrite on X (other packages untouched) |
| `forja alias set NAME PKG` | Register a short name to use anywhere `--pkg` is accepted (writes to `forja/aliases.local.yml` — a personal file you should gitignore) |
| `forja alias rm NAME` | Delete an alias |
| `forja alias list` | List registered aliases |

Every command that accepts `--pkg` **takes either an alias or a full package name**. Unknown inputs pass through as literal package names, so things still work when no alias is set:

```bash
forja alias set dev com.tkhskt.forja.sample
forja apply --pkg dev --enable teapot         # "dev" resolves to com.tkhskt.forja.sample
forja apply --pkg com.acme.plugin --enable y  # unknown alias → treated as a literal pkg
```

---

## Shared flags on `rules add / update`

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

---

## Scope conflict resolution

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

### `forja/status.json`

> **Don't edit this file by hand.** It's a CLI-managed mirror of what's currently pushed to each device. Manual edits won't reach the device and may be overwritten on the next `forja` invocation. The commands that write it are `forja apply`, the `rules` TUI's save action, `forja off`, `forja rules add --pkg X` (the sugar form), and `forja rules remove`.
>
> The same warning ships inside the file as a top-level `$comment` key (JSON Schema-style metadata) so anyone opening it in an editor sees it on line 1. Any `$`-prefixed key is silently dropped on load, so the convention is forward-compatible with additional metadata (`$schema` etc.) without forja ever interpreting those entries as package names.

The **per-package enabled rule list**: "if `name` is in pkg X's enabled list, the rule is on; otherwise it's off." Two values, no middle ground:

```json
{
  "$comment": "THIS FILE IS GENERATED BY forja. DO NOT EDIT BY HAND.",
  "com.tkhskt.sample_app": {
    "enabled": ["mock-failure", "big-response"]
  },
  "com.tkhskt.sample_app.staging": {
    "enabled": ["mock-failure"]
  }
}
```

`"enabled": []` (an empty array) means "forja has touched this pkg but nothing is enabled right now" — that's the state right after `forja off --pkg X`. If the pkg key itself is absent, forja has never interacted with that pkg.

---

## Recommended `.gitignore`

forja does not edit `.gitignore` for you. When sharing across a team, add these lines yourself:

```gitignore
# forja: don't commit personal rules / state / aliases
forja/rules.local.yml
forja/status.json
forja/aliases.local.yml
```
