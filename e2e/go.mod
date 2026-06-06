// e2e is a separate Go module so its build tag (//go:build e2e) and emulator
// dependencies don't bleed into the main `cli` module's `go test ./...`. Run
// it explicitly via `go test -tags e2e ./e2e/...` from the e2e directory.
module github.com/tkhskt/forja/e2e

go 1.25
