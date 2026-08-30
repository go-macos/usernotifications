// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build darwin

package usernotifications_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The live suite. It is the only place the real UNUserNotificationCenter is
// reached, and reaching it takes more than a Go test binary can be:
//
//  1. an .app bundle with a bundle identifier — without one the framework
//     throws and the process dies, which is the whole subject of this package;
//  2. a code signature, even an ad-hoc one — an unsigned bundle gets
//     UNErrorDomain error 1, "notifications are not allowed";
//  3. registration with LaunchServices — a signed bundle the system has never
//     seen is refused the same way.
//
// All three were established by measurement, not from documentation, and each
// one produced a different failure. So this suite assembles the bundle around
// cmd/unsend, signs it, registers it, and drives the real thing end to end.
//
// It SKIPS rather than fails when the machine cannot support it — no toolchain,
// no codesign, no LaunchServices, or a notification daemon that refuses to
// authorize — because those are properties of the runner, not defects in the
// package. What it never skips is the guard: TestLiveBareBinaryRefusesInsteadOfCrashing
// runs the unbundled executable and requires it to survive.

const (
	// A stable identifier, deliberately. Each distinct bundle identifier that
	// asks for authorization becomes its own row in System Settings ▸
	// Notifications, so a per-run random one would litter the user's machine.
	liveBundleID = "io.github.go-macos.usernotifications.livetest"
	liveAppName  = "unsend-livetest"
)

// runTool runs one unsend subcommand and decodes its JSON result.
func runTool(t *testing.T, exe string, args ...string) (map[string]any, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if dir := coverDir(); dir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
		if len(ee.Stderr) > 0 {
			t.Logf("unsend %v stderr: %s", args, ee.Stderr)
		}
	} else if err != nil {
		t.Fatalf("unsend %v: %v", args, err)
	}
	var m map[string]any
	if jsonErr := json.Unmarshal(out, &m); jsonErr != nil {
		t.Fatalf("unsend %v printed %q, which is not the JSON object it promises: %v", args, out, jsonErr)
	}
	t.Logf("unsend %v -> exit %d %s", args, code, strings.TrimSpace(string(out)))
	return m, code
}

// buildTool compiles cmd/unsend to dst.
func buildTool(t *testing.T, dst string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	args := []string{"build"}
	// When the caller sets GOCOVERDIR, build the helper INSTRUMENTED. The live
	// suite drives the darwin bindings entirely from a subprocess — that is the
	// only place a bundled process can exist — so without this their coverage is
	// simply not measured, and "the bindings are untested" and "the bindings are
	// untestable" look identical in a report. `go tool covdata textfmt` turns
	// what lands in GOCOVERDIR into a profile that merges with this package's.
	if coverDir() != "" {
		// "./..." and not the import path: -coverpkg with an explicit import
		// path that does not include the MAIN package produces a binary that
		// writes nothing at all, silently.
		args = append(args, "-cover", "-coverpkg=./...")
	}
	args = append(args, "-o", dst, "./cmd/unsend")
	cmd := exec.Command("go", args...)
	// GOWORK=off deliberately: this module is not a member of any workspace,
	// and inheriting one that does not list it turns a build into a confusing
	// "directory prefix does not contain modules" failure.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/unsend: %v\n%s", err, out)
	}
}

// coverDir is where an instrumented helper should drop its coverage data, or ""
// to build it uninstrumented.
//
// It is read from UN_LIVE_COVERDIR rather than GOCOVERDIR because `go test`
// owns GOCOVERDIR in the test process and does not hand it through: a variable
// this suite sets on the child itself is the only one it can rely on.
func coverDir() string { return os.Getenv("UN_LIVE_COVERDIR") }

// bareTool builds the unbundled executable.
func bareTool(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "unsend")
	buildTool(t, exe)
	return exe
}

// liveDir is the durable directory the test bundle is assembled in.
//
// NOT t.TempDir(), and this is a measured requirement rather than a preference:
// an .app under /var/folders (which is what t.TempDir() returns) is registered
// by lsregister without complaint and then refused by the notification system
// with "Notifications are not allowed for this application", while the byte-
// identical bundle under ~/Library/Application Support is granted immediately.
// The temp path costs an hour if you assume the code is wrong.
//
// It also must not be anywhere inside a repository, so the walk-up below fails
// the test rather than leaving a signed executable one `git add -A` from being
// committed.
func liveDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GO_MACOS_UN_LIVE_DIR")
	if base == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			t.Skipf("no user config directory to build the test bundle in: %v", err)
		}
		base = filepath.Join(cfg, "go-macos-usernotifications")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", base, err)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		t.Fatalf("abs %s: %v", base, err)
	}
	for dir := abs; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Fatalf("refusing to build the test bundle at %s: it is inside the work tree %s. "+
				"A signed executable written into a repository is one `git add -A` from being published.", abs, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return abs
}

// bundledTool assembles, signs and registers an .app around cmd/unsend and
// returns the path of the executable inside it.
//
// The Info.plist is written by hand here rather than with
// github.com/go-macos/appbundle — which is the library for this and what the
// README tells a user to reach for — only so that this package's test suite
// does not depend on another module to prove its own subject. The keys are the
// same ones appbundle.Spec produces.
func bundledTool(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign is not available; the bundle cannot be signed and the system would refuse it")
	}
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/" +
		"LaunchServices.framework/Support/lsregister"
	if _, err := os.Stat(lsregister); err != nil {
		t.Skipf("lsregister is not present at %s; the bundle cannot be registered", lsregister)
	}

	app := filepath.Join(liveDir(t), liveAppName+".app")
	t.Logf("live bundle: %s", app)
	if err := os.RemoveAll(app); err != nil {
		t.Fatalf("clearing %s: %v", app, err)
	}
	macOS := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>%s</string>
	<key>CFBundleExecutable</key><string>%s</string>
	<key>CFBundleIdentifier</key><string>%s</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>0.1.0</string>
	<key>CFBundleVersion</key><string>0.1.0</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>NSPrincipalClass</key><string>NSApplication</string>
</dict>
</plist>
`, liveAppName, liveAppName, liveBundleID)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatalf("Info.plist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "PkgInfo"), []byte("APPL????"), 0o644); err != nil {
		t.Fatalf("PkgInfo: %v", err)
	}

	exe := filepath.Join(macOS, liveAppName)
	buildTool(t, exe)

	// An ad-hoc signature is enough, and it is the difference between working
	// and UNErrorDomain error 1.
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Skipf("codesign refused the bundle (%v): %s", err, out)
	}
	if out, err := exec.Command(lsregister, "-f", app).CombinedOutput(); err != nil {
		t.Skipf("lsregister refused the bundle (%v): %s", err, out)
	}
	// Tidy up after a PASS, and leave the evidence after a failure: the point of
	// a durable directory is that somebody can go and look at what broke.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("test failed; leaving the bundle at %s for inspection", app)
			return
		}
		_ = exec.Command(lsregister, "-u", app).Run()
		_ = os.RemoveAll(app)
	})
	return exe
}

// authorize gets the app past authorization, quietly, and skips if the machine
// will not grant it.
func authorize(t *testing.T, exe string) {
	t.Helper()
	// Provisional: granted with no dialog and no user present. A non-provisional
	// request would put a modal prompt on somebody's screen and, worse, an
	// unanswered prompt is recorded by the OS as a permanent REFUSAL.
	res, code := runTool(t, exe, "auth", "-provisional", "-timeout", "30s")
	if code != 0 {
		t.Skipf("the system would not grant provisional authorization: %v", res["error"])
	}
	if granted, _ := res["granted"].(bool); !granted {
		t.Skip("provisional authorization was refused by this machine")
	}
}

// listDelivered returns the delivered notifications keyed by identifier.
func listDelivered(t *testing.T, exe string) map[string]map[string]any {
	t.Helper()
	res, code := runTool(t, exe, "list")
	if code != 0 {
		t.Fatalf("unsend list: %v", res["error"])
	}
	got := map[string]map[string]any{}
	items, _ := res["notifications"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		id, _ := m["ID"].(string)
		got[id] = m
	}
	return got
}

// waitForDelivered polls until the delivered set has n entries, or gives up.
// Delivery is asynchronous on the system's side: addNotificationRequest's
// completion handler reports that the request was ACCEPTED, and the
// notification appears in Notification Center a moment later.
func waitForDelivered(t *testing.T, exe string, n int) map[string]map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var got map[string]map[string]any
	for time.Now().Before(deadline) {
		got = listDelivered(t, exe)
		if len(got) == n {
			return got
		}
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatalf("delivered set never reached %d entries; last saw %d: %v", n, len(got), got)
	return nil
}

// TestLiveBareBinaryRefusesInsteadOfCrashing is the guard test, and it does not
// skip.
//
// Run outside an .app, the tool must report ErrNotBundled and EXIT — not abort.
// Before the bundle guard existed this same binary died with
// "Terminating app due to uncaught exception 'NSInternalInconsistencyException'
// … bundleProxyForCurrentProcess is nil" and SIGABRT, which is exit status 2
// with a Go traceback rather than the exit status 1 and a JSON object asserted
// here.
func TestLiveBareBinaryRefusesInsteadOfCrashing(t *testing.T) {
	exe := bareTool(t)
	res, code := runTool(t, exe, "check")
	if code != 0 {
		t.Fatalf("check exited %d; it must succeed and simply report unavailability", code)
	}
	if avail, _ := res["available"].(bool); avail {
		t.Fatalf("check reports available=true from a bare executable: %v", res)
	}
	if bid, _ := res["bundleIdentifier"].(string); bid != "" {
		t.Fatalf("bundleIdentifier = %q from a bare executable", bid)
	}
	if reason, _ := res["reason"].(string); !strings.Contains(reason, "bundle") {
		t.Fatalf("reason = %q; it must say the process is not in a bundle", reason)
	}

	// Every entry point, not just check: none of them may reach the center.
	for _, args := range [][]string{
		{"post", "-title", "should not appear"},
		{"list"},
		{"list", "-pending"},
		{"settings"},
		{"auth", "-provisional", "-timeout", "5s"},
		{"remove", "anything"},
		{"remove-all"},
	} {
		res, code := runTool(t, exe, args...)
		if code != 1 {
			t.Errorf("unsend %v exited %d, want 1 with an error object", args, code)
			continue
		}
		if notBundled, _ := res["notBundled"].(bool); !notBundled {
			t.Errorf("unsend %v reported %v, want the not-bundled error", args, res["error"])
		}
	}
}

// TestLiveRoundTrip is the real thing: authorize, post, read back from the
// system, remove, and read back again.
func TestLiveRoundTrip(t *testing.T) {
	exe := bundledTool(t)

	res, code := runTool(t, exe, "check")
	if code != 0 {
		t.Fatalf("check exited %d: %v", code, res["error"])
	}
	if avail, _ := res["available"].(bool); !avail {
		t.Fatalf("the bundled tool still reports unavailable: %v", res["reason"])
	}
	if bid, _ := res["bundleIdentifier"].(string); bid != liveBundleID {
		t.Fatalf("bundleIdentifier = %q, want %q", bid, liveBundleID)
	}

	authorize(t, exe)

	if res, code := runTool(t, exe, "settings"); code != 0 {
		t.Fatalf("settings exited %d: %v", code, res["error"])
	} else if status, _ := res["status"].(string); status != "provisional" && status != "authorized" {
		t.Fatalf("authorization status = %q after a granted request", status)
	}

	// Start from a known state.
	if res, code := runTool(t, exe, "remove-all"); code != 0 {
		t.Fatalf("remove-all exited %d: %v", code, res["error"])
	}
	waitForDelivered(t, exe, 0)

	// Post two, one of them with every field set.
	if res, code := runTool(t, exe, "post",
		"-id", "alpha", "-title", "Alpha", "-subtitle", "Sub", "-body", "Body of alpha",
		"-sound", "default"); code != 0 {
		t.Fatalf("post alpha exited %d: %v", code, res["error"])
	} else if id, _ := res["id"].(string); id != "alpha" {
		t.Fatalf("post returned id %q, want alpha", id)
	}

	generated := ""
	if res, code := runTool(t, exe, "post", "-title", "Beta", "-body", "Body of beta"); code != 0 {
		t.Fatalf("post beta exited %d: %v", code, res["error"])
	} else {
		generated, _ = res["id"].(string)
		if len(generated) != 32 {
			t.Fatalf("post with no identifier returned %q, want 32 generated hex characters", generated)
		}
	}

	// Read back from the SYSTEM. This is the assertion that matters: the
	// package reports what UNUserNotificationCenter is actually holding, which
	// no amount of "post returned nil" can stand in for.
	got := waitForDelivered(t, exe, 2)
	alpha, ok := got["alpha"]
	if !ok {
		t.Fatalf("alpha is not in the delivered set: %v", got)
	}
	for field, want := range map[string]string{"Title": "Alpha", "Subtitle": "Sub", "Body": "Body of alpha"} {
		if v, _ := alpha[field].(string); v != want {
			t.Errorf("delivered alpha %s = %q, want %q", field, v, want)
		}
	}
	if beta, ok := got[generated]; !ok {
		t.Errorf("the generated identifier %q is not in the delivered set: %v", generated, got)
	} else if v, _ := beta["Title"].(string); v != "Beta" {
		t.Errorf("delivered beta title = %q, want Beta", v)
	}

	// Posting the same identifier again REPLACES rather than adds.
	if res, code := runTool(t, exe, "post", "-id", "alpha", "-title", "Alpha again"); code != 0 {
		t.Fatalf("re-post alpha exited %d: %v", code, res["error"])
	}
	got = waitForDelivered(t, exe, 2)
	if v, _ := got["alpha"]["Title"].(string); v != "Alpha again" {
		t.Errorf("after re-posting alpha its title is %q, want %q", v, "Alpha again")
	}

	// Remove one by identifier.
	if res, code := runTool(t, exe, "remove", "alpha"); code != 0 {
		t.Fatalf("remove exited %d: %v", code, res["error"])
	}
	got = waitForDelivered(t, exe, 1)
	if _, still := got["alpha"]; still {
		t.Error("alpha survived remove")
	}
	if _, ok := got[generated]; !ok {
		t.Errorf("remove took the wrong notification: %v", got)
	}

	// And remove the rest.
	if res, code := runTool(t, exe, "remove-all"); code != 0 {
		t.Fatalf("remove-all exited %d: %v", code, res["error"])
	}
	waitForDelivered(t, exe, 0)
}

// TestLivePendingIsEmpty pins the pending path against a live center. This
// package posts everything with a nil trigger, so nothing it sends is ever
// pending — and the getter must say so rather than reporting the delivered set.
func TestLivePendingIsEmpty(t *testing.T) {
	exe := bundledTool(t)
	authorize(t, exe)
	t.Cleanup(func() { runTool(t, exe, "remove-all") })

	if res, code := runTool(t, exe, "post", "-id", "pending-probe", "-title", "Probe"); code != 0 {
		t.Fatalf("post exited %d: %v", code, res["error"])
	}
	waitForDelivered(t, exe, 1)

	res, code := runTool(t, exe, "list", "-pending")
	if code != 0 {
		t.Fatalf("list -pending exited %d: %v", code, res["error"])
	}
	if n, _ := res["count"].(float64); n != 0 {
		t.Fatalf("pending count = %v; a nil-trigger notification is delivered, never pending: %v", n, res)
	}
}
