# Installation

Three ways to install forja, from easiest to most involved:

1. [Bash installer](#bash-installer) (macOS / Linux)
2. [Windows manual install](#windows)
3. [Build from source](#build-from-source)

After install, every `forja` command resolves the JVMTI agent bundle (the four ABI `.so` files plus `agent-bundle.dex`) via the [bundle search order](#bundle-search-order).

For [updating](#updating), see the bottom of this page.

---

## Bash installer

```bash
curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

- Installs the binary to `$HOME/.local/bin/forja` and the JVMTI agent to `$HOME/.local/share/forja/agent/`.
- Supports **macOS arm64 / amd64** and **Linux arm64 / amd64**.
- No `sudo` required.

System-wide install:

```bash
PREFIX=/usr/local curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | sudo bash
```

Pin a specific version:

```bash
FORJA_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

If `~/.local/bin` isn't on your `PATH` yet, add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Verify:

```bash
forja --version
forja --help
```

Then run `forja init` at the root of the project you want to use forja in (one time per project):

```bash
cd path/to/your/project
forja init    # creates ./forja/rules.yml with a schema-commented template
              # and prints the recommended .gitignore entries (does not edit .gitignore)
```

forja refuses to do anything before `init` has run, to keep an accidental `forja rules add` in the wrong directory from silently materializing an orphan `forja/` somewhere unexpected.

---

## Windows

The release page also ships `forja_<version>_windows_amd64.zip` and `forja_<version>_windows_arm64.zip`. The bash installer above does not run on native Windows — extract the archive manually:

1. Download the `.zip` from [Releases](https://github.com/tkhskt/forja/releases) and extract it (Explorer can do this natively).
2. Copy `bin/forja.exe` somewhere on your `PATH` (e.g. `%USERPROFILE%\.local\bin\forja.exe`).
3. Set `FORJA_BUNDLE_DIR` to the extracted `share/forja/agent/` directory, or move the agent files to `%USERPROFILE%\.local\share\forja\agent\` so forja can discover them automatically.

Windows users can also run the installer from WSL — it behaves identically to the Linux build there.

---

## Build from source

Requirements when building from source:

- Go 1.25+
- Android SDK + NDK (for the JVMTI agent's native build)
- JDK 17+
- macOS / Linux (Windows source builds are not regularly tested)

```bash
git clone https://github.com/tkhskt/forja
cd forja
./gradlew :jvmti-agent:bundleAgentDex    # JVMTI agent → jvmti-agent/build/outputs/agent/
(cd cli && go build -o ../bin/forja .)   # CLI binary → ./bin/forja
./bin/forja --help
```

When you run forja from inside the repo, it auto-discovers `./jvmti-agent/build/outputs/agent/` — no environment variable required.

---

## Bundle search order

Each `forja` command resolves the agent bundle directory in this order — the first that exists wins:

1. `--bundle DIR` flag
2. `$FORJA_BUNDLE_DIR`
3. `$XDG_DATA_HOME/forja/agent`
4. `$HOME/.local/share/forja/agent` ← installer default
5. `/usr/local/share/forja/agent`
6. `./jvmti-agent/build/outputs/agent` (fallback for in-repo development)

If none exists, forja exits with an error telling you to install, set `FORJA_BUNDLE_DIR`, or build from source.

---

## Updating

### macOS / Linux

The same bash one-liner is the update command:

```bash
curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

install.sh always re-resolves the latest tag via the GitHub API and overwrites the binary. The agent directory under `$PREFIX/share/forja/agent/` is **wiped and recopied** on every run, so any `.so` file removed in a future release won't linger from a previous install.

To pin a specific version (e.g. for downgrading or reproducing a bug report):

```bash
FORJA_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/tkhskt/forja/main/install.sh | bash
```

Confirm the running version:

```bash
forja --version
```

### Windows

Re-download the matching `.zip` from [Releases](https://github.com/tkhskt/forja/releases) and replace `bin/forja.exe` plus the contents of your agent directory (whatever `FORJA_BUNDLE_DIR` points at, or `%USERPROFILE%\.local\share\forja\agent\` by default). Deleting the old agent directory before extracting the new one is recommended for the same "no lingering files" reason as the bash installer.
