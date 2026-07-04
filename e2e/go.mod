// e2e is a separate Go module so its build tag (//go:build e2e) and emulator
// dependencies don't bleed into the main `cli` module's `go test ./...`. Run
// it explicitly via `go test -tags e2e ./e2e/...` from the e2e directory.
module github.com/tkhskt/forja/e2e

go 1.25.0

require github.com/modelcontextprotocol/go-sdk v1.6.1

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
