# Vendored slicer

This directory is a verbatim copy of the **slicer** library from AOSP's
`platform/tools/dexter` repository — a dex bytecode reader/writer/instrumenter.

- Upstream: https://android.googlesource.com/platform/tools/dexter
- Path in upstream: `slicer/`
- Commit: `d992a222ec28b56efa29f9104db060379298049c`
- License: **Apache License, Version 2.0** (each source file retains its
  original license header; the upstream repo ships no separate top-level
  LICENSE/NOTICE file). Forja itself is also Apache-2.0 — see the root
  [`LICENSE`](../../../../../LICENSE) and [`NOTICE`](../../../../../NOTICE).

## Why it's here

forja instruments `okhttp3.OkHttpClient.interceptors()` with an exit hook so
the getter's return value is routed through `Bootstrap.wrapInterceptors`,
prepending a `RulesInterceptor`. This is the same dex-rewriting technique
Android Studio's NetworkInspector (ArtTooling) uses, and it keeps the hooked
method JIT-compiled (no breakpoint, no deopt, no heap walk). See
[`../agent.cpp`](../agent.cpp) for the integration and
[`../../../../../docs/internals.md`](../../../../../docs/internals.md) for the
overall flow.

## Updating

Re-copy `slicer/*.cc` and `slicer/export/slicer/*.h` from the upstream commit
you want, then rebuild with `./gradlew :jvmti-agent:bundleAgentDex`. No local
modifications are made to the slicer sources, so updates are a clean overwrite.
