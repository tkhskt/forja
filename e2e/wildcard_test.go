//go:build e2e

// Wildcard path-matching e2e — proves a `*` in a rule's --path travels
// CLI → device JSON → on-device interceptor and fires against a real request
// whose middle path segment (an id) differs from the rule literal.
package e2e_test

import (
	"testing"
	"time"
)

// TestWildcardPathMatch drives the fixture against /users/<id>/posts and
// /users/<id>/comments under a single `--path /users/*/posts` rule:
//
//   - positive: /users/42/posts matches the glob → response rewritten (418)
//   - negative: /users/42/comments does NOT match → baseline passthrough (200)
//
// The two halves together show the wildcard is real (it matched an id the rule
// never named) AND segment-precise (it didn't rewrite a sibling endpoint).
func TestWildcardPathMatch(t *testing.T) {
	resetForjaState(t, AppDev)

	runForja(t, "rules", "add", "wild-posts",
		"--host", "127.0.0.1", "--path", "/users/*/posts",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)

	// --- positive: /users/42/posts → matches /users/*/posts → 418 -------
	forceStop(t, AppDev)
	startMainActivityWithPath(t, AppDev, "/users/42/posts")
	clearLogcat(t)
	runForja(t, "apply", "--app", AppDev, "--enable", "wild-posts")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*rewritten.*"
`)
	waitForLogcat(t, "hit 'wild-posts'", 10*time.Second, "Forja")

	// --- negative: /users/42/comments → no match → baseline 200 ---------
	// Same rule still enabled; relaunch on a sibling endpoint and re-push.
	forceStop(t, AppDev)
	startMainActivityWithPath(t, AppDev, "/users/42/comments")
	clearLogcat(t)
	runForja(t, "apply", "--app", AppDev, "--enable", "wild-posts")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// If the wildcard wrongly matched, this would render HTTP 418 and the
	// wait below would time out — that timeout IS the assertion.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 200.*"
    timeout: 30000
`)
}
