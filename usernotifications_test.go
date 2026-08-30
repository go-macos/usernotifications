// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package usernotifications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests run on EVERY platform. They drive the whole package through the
// seams declared in usernotifications.go, replaced here with fakes, so that
// every guard and every error branch is exercised without an .app bundle, a
// code signature, a window server or even a Mac. The real bindings underneath
// the seams are exercised separately: bridge_darwin_test.go for the parts that
// are safe outside a bundle, live_darwin_test.go for the ones that are not.

// fakeOS is a scriptable stand-in for the whole darwin bridge.
type fakeOS struct {
	loadErr   error
	bundleID  string
	center    uintptr
	content   uintptr
	request   uintptr
	released  []uintptr
	silentAdd bool // never call the addRequest completion (drives the ctx branch)

	authGranted bool
	authMsg     string
	silentAuth  bool

	settings     Settings
	settingsOK   bool
	silentGet    bool
	recs         []Record
	recsOK       bool
	silentRecs   bool
	addMsg       string
	removedIDs   []string
	removedAll   int
	pendingAsked []bool
	newContentIn []Notification
	newRequestIn []string
}

// install points every seam at f and restores the originals when the test ends.
func (f *fakeOS) install(t *testing.T) *fakeOS {
	t.Helper()
	origs := struct {
		load     func() error
		bundle   func() string
		center   func() uintptr
		content  func(Notification) uintptr
		request  func(string, uintptr) uintptr
		release  func(uintptr)
		auth     func(uintptr, uint64, func(bool, string))
		settings func(uintptr, func(Settings, bool))
		add      func(uintptr, uintptr, func(string))
		recs     func(uintptr, bool, func([]Record, bool))
		rmIDs    func(uintptr, []string)
		rmAll    func(uintptr)
	}{
		loadFrameworks, bundleIdentifier, currentCenter, newContent, newRequest,
		releaseObject, requestAuth, fetchSettings, addRequest, fetchRecords,
		removeIdentifiers, removeEverything,
	}
	t.Cleanup(func() {
		loadFrameworks, bundleIdentifier, currentCenter = origs.load, origs.bundle, origs.center
		newContent, newRequest, releaseObject = origs.content, origs.request, origs.release
		requestAuth, fetchSettings, addRequest = origs.auth, origs.settings, origs.add
		fetchRecords, removeIdentifiers, removeEverything = origs.recs, origs.rmIDs, origs.rmAll
	})

	loadFrameworks = func() error { return f.loadErr }
	bundleIdentifier = func() string { return f.bundleID }
	currentCenter = func() uintptr { return f.center }
	newContent = func(n Notification) uintptr {
		f.newContentIn = append(f.newContentIn, n)
		return f.content
	}
	newRequest = func(id string, _ uintptr) uintptr {
		f.newRequestIn = append(f.newRequestIn, id)
		return f.request
	}
	releaseObject = func(o uintptr) { f.released = append(f.released, o) }
	requestAuth = func(_ uintptr, _ uint64, done func(bool, string)) {
		if f.silentAuth {
			return
		}
		done(f.authGranted, f.authMsg)
	}
	fetchSettings = func(_ uintptr, done func(Settings, bool)) {
		if f.silentGet {
			return
		}
		done(f.settings, f.settingsOK)
	}
	addRequest = func(_ uintptr, _ uintptr, done func(string)) {
		if f.silentAdd {
			return
		}
		done(f.addMsg)
	}
	fetchRecords = func(_ uintptr, pending bool, done func([]Record, bool)) {
		f.pendingAsked = append(f.pendingAsked, pending)
		if f.silentRecs {
			return
		}
		done(f.recs, f.recsOK)
	}
	removeIdentifiers = func(_ uintptr, ids []string) { f.removedIDs = append(f.removedIDs, ids...) }
	removeEverything = func(uintptr) { f.removedAll++ }
	return f
}

// workingOS is a fake in which everything succeeds.
func workingOS(t *testing.T) *fakeOS {
	return (&fakeOS{
		bundleID: "io.github.go-macos.test", center: 0xC, content: 0xA, request: 0xB,
		settingsOK: true, recsOK: true, authGranted: true,
	}).install(t)
}

// cancelled returns a context that is already done, so the select in every
// asynchronous entry point takes its ctx branch deterministically.
func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// ---------------------------------------------------------------------------
// Enum rendering.
// ---------------------------------------------------------------------------

func TestStatusString(t *testing.T) {
	for _, c := range []struct {
		in   Status
		want string
	}{
		{StatusNotDetermined, "not-determined"},
		{StatusDenied, "denied"},
		{StatusAuthorized, "authorized"},
		{StatusProvisional, "provisional"},
		{StatusEphemeral, "ephemeral"},
		{Status(42), "status(42)"},
	} {
		if got := c.in.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", int(c.in), got, c.want)
		}
	}
}

func TestSettingString(t *testing.T) {
	for _, c := range []struct {
		in   Setting
		want string
	}{
		{SettingNotSupported, "not-supported"},
		{SettingDisabled, "disabled"},
		{SettingEnabled, "enabled"},
		{Setting(9), "setting(9)"},
	} {
		if got := c.in.String(); got != c.want {
			t.Errorf("Setting(%d).String() = %q, want %q", int(c.in), got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation.
// ---------------------------------------------------------------------------

func TestNotificationValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Notification
		want error
	}{
		{"ok", Notification{Title: "t"}, nil},
		{"okFull", Notification{ID: "i", Title: "t", Subtitle: "s", Body: "b", Sound: DefaultSound}, nil},
		{"emptyTitle", Notification{Body: "b"}, ErrEmptyTitle},
		{"idTooLong", Notification{ID: strings.Repeat("a", MaxIdentifierLen+1), Title: "t"}, ErrIDTooLong},
		{"idExactlyMax", Notification{ID: strings.Repeat("a", MaxIdentifierLen), Title: "t"}, nil},
		{"nulID", Notification{ID: "a\x00b", Title: "t"}, ErrHasNUL},
		{"nulTitle", Notification{Title: "a\x00b"}, ErrHasNUL},
		{"nulSubtitle", Notification{Title: "t", Subtitle: "a\x00b"}, ErrHasNUL},
		{"nulBody", Notification{Title: "t", Body: "a\x00b"}, ErrHasNUL},
		{"nulSound", Notification{Title: "t", Sound: Sound("a\x00b")}, ErrHasNUL},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.validate(); !errors.Is(got, c.want) {
				t.Fatalf("validate() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestValidateNamesTheOffendingField pins that the NUL error says WHICH field
// was wrong. The fields are checked through a map, so the message is the only
// thing that tells a caller where to look.
func TestValidateNamesTheOffendingField(t *testing.T) {
	err := Notification{Title: "t", Body: "a\x00b"}.validate()
	if err == nil || !strings.Contains(err.Error(), "Body") {
		t.Fatalf("validate() = %v, want a message naming Body", err)
	}
}

// ---------------------------------------------------------------------------
// center(): the guard sequence, which is the reason this package exists.
// ---------------------------------------------------------------------------

func TestCenterLoadFailure(t *testing.T) {
	boom := errors.New("no frameworks")
	f := workingOS(t)
	f.loadErr = boom
	if _, err := center(); !errors.Is(err, boom) {
		t.Fatalf("center() = %v, want %v", err, boom)
	}
}

// TestCenterRefusesWithoutBundleIdentity is the important one. Without a bundle
// identifier the package must report ErrNotBundled and must NOT go on to ask
// for the center: that send throws an uncatchable Objective-C exception.
func TestCenterRefusesWithoutBundleIdentity(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	asked := false
	currentCenter = func() uintptr { asked = true; return f.center }

	if _, err := center(); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("center() = %v, want ErrNotBundled", err)
	}
	if asked {
		t.Fatal("currentNotificationCenter was reached with no bundle identifier; " +
			"on a real Mac that is SIGABRT, not an error value")
	}
}

func TestCenterNilCenterIsDistinctFromNotBundled(t *testing.T) {
	f := workingOS(t)
	f.center = 0
	_, err := center()
	if !errors.Is(err, ErrNoCenter) {
		t.Fatalf("center() = %v, want ErrNoCenter", err)
	}
	if errors.Is(err, ErrNotBundled) {
		t.Fatal("a nil center must not be reported as ErrNotBundled: different causes, different fixes")
	}
}

func TestCenterSucceeds(t *testing.T) {
	f := workingOS(t)
	c, err := center()
	if err != nil || c != f.center {
		t.Fatalf("center() = %#x, %v; want %#x, nil", c, err, f.center)
	}
}

// ---------------------------------------------------------------------------
// Available / BundleIdentifier.
// ---------------------------------------------------------------------------

func TestAvailable(t *testing.T) {
	f := workingOS(t)
	if err := Available(); err != nil {
		t.Fatalf("Available() = %v, want nil", err)
	}
	f.bundleID = ""
	if err := Available(); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("Available() unbundled = %v, want ErrNotBundled", err)
	}
}

func TestBundleIdentifier(t *testing.T) {
	f := workingOS(t)
	if got := BundleIdentifier(); got != f.bundleID {
		t.Fatalf("BundleIdentifier() = %q, want %q", got, f.bundleID)
	}
	f.loadErr = errors.New("no frameworks")
	if got := BundleIdentifier(); got != "" {
		t.Fatalf("BundleIdentifier() with no frameworks = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Identifier generation.
// ---------------------------------------------------------------------------

func TestNewIdentifier(t *testing.T) {
	a, err := newIdentifier()
	if err != nil {
		t.Fatalf("newIdentifier() = %v", err)
	}
	if len(a) != 32 {
		t.Fatalf("newIdentifier() = %q, want 32 hex characters", a)
	}
	b, _ := newIdentifier()
	if a == b {
		t.Fatalf("newIdentifier() returned the same value twice: %q", a)
	}
}

func TestNewIdentifierRandomnessFailure(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	boom := errors.New("no entropy")
	randRead = func([]byte) (int, error) { return 0, boom }

	if _, err := newIdentifier(); !errors.Is(err, boom) {
		t.Fatalf("newIdentifier() = %v, want %v", err, boom)
	}
}

// ---------------------------------------------------------------------------
// RequestAuthorization.
// ---------------------------------------------------------------------------

func TestRequestAuthorizationGranted(t *testing.T) {
	f := workingOS(t)
	f.authGranted = true
	granted, err := RequestAuthorization(context.Background(), OptionAlert|OptionSound)
	if err != nil || !granted {
		t.Fatalf("RequestAuthorization() = %v, %v; want true, nil", granted, err)
	}
}

// TestRequestAuthorizationRefusedIsNotAnError pins that a plain "no" from the
// user is reported as granted=false with a nil error.
func TestRequestAuthorizationRefusedIsNotAnError(t *testing.T) {
	f := workingOS(t)
	f.authGranted = false
	granted, err := RequestAuthorization(context.Background(), OptionAlert)
	if err != nil {
		t.Fatalf("RequestAuthorization() refused = %v, want a nil error", err)
	}
	if granted {
		t.Fatal("granted = true, want false")
	}
}

func TestRequestAuthorizationSystemError(t *testing.T) {
	f := workingOS(t)
	f.authMsg = "Notifications are not allowed for this application"
	_, err := RequestAuthorization(context.Background(), OptionAlert)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("RequestAuthorization() = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error %q does not carry the system's own message", err)
	}
}

func TestRequestAuthorizationOptionsReachTheOS(t *testing.T) {
	workingOS(t)
	var got uint64
	requestAuth = func(_ uintptr, opts uint64, done func(bool, string)) {
		got = opts
		done(true, "")
	}
	if _, err := RequestAuthorization(context.Background(), OptionProvisional|OptionAlert); err != nil {
		t.Fatalf("RequestAuthorization() = %v", err)
	}
	if want := uint64(1<<6 | 1<<2); got != want {
		t.Fatalf("options = %#b, want %#b", got, want)
	}
}

func TestRequestAuthorizationContextDone(t *testing.T) {
	f := workingOS(t)
	f.silentAuth = true
	if _, err := RequestAuthorization(cancelled(), OptionAlert); !errors.Is(err, context.Canceled) {
		t.Fatalf("RequestAuthorization() = %v, want context.Canceled", err)
	}
}

func TestRequestAuthorizationNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if _, err := RequestAuthorization(context.Background(), OptionAlert); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("RequestAuthorization() = %v, want ErrNotBundled", err)
	}
}

// ---------------------------------------------------------------------------
// CurrentSettings.
// ---------------------------------------------------------------------------

func TestCurrentSettings(t *testing.T) {
	f := workingOS(t)
	f.settings = Settings{Status: StatusProvisional, Alert: SettingDisabled, Sound: SettingEnabled, Badge: SettingEnabled}
	got, err := CurrentSettings(context.Background())
	if err != nil {
		t.Fatalf("CurrentSettings() = %v", err)
	}
	if got != f.settings {
		t.Fatalf("CurrentSettings() = %+v, want %+v", got, f.settings)
	}
}

func TestCurrentSettingsNilObject(t *testing.T) {
	f := workingOS(t)
	f.settingsOK = false
	_, err := CurrentSettings(context.Background())
	if !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("CurrentSettings() = %v, want ErrObjectMissing", err)
	}
	if !strings.Contains(err.Error(), "UNNotificationSettings") {
		t.Fatalf("error %q does not name the missing object", err)
	}
}

func TestCurrentSettingsContextDone(t *testing.T) {
	f := workingOS(t)
	f.silentGet = true
	if _, err := CurrentSettings(cancelled()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CurrentSettings() = %v, want context.Canceled", err)
	}
}

func TestCurrentSettingsNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if _, err := CurrentSettings(context.Background()); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("CurrentSettings() = %v, want ErrNotBundled", err)
	}
}

// ---------------------------------------------------------------------------
// Post.
// ---------------------------------------------------------------------------

func TestPost(t *testing.T) {
	f := workingOS(t)
	n := Notification{ID: "alpha", Title: "Alpha", Subtitle: "sub", Body: "body", Sound: DefaultSound}
	id, err := Post(context.Background(), n)
	if err != nil {
		t.Fatalf("Post() = %v", err)
	}
	if id != "alpha" {
		t.Fatalf("Post() id = %q, want %q", id, "alpha")
	}
	if len(f.newContentIn) != 1 || f.newContentIn[0] != n {
		t.Fatalf("newContent got %+v, want %+v", f.newContentIn, n)
	}
	if len(f.newRequestIn) != 1 || f.newRequestIn[0] != "alpha" {
		t.Fatalf("newRequest got %v, want [alpha]", f.newRequestIn)
	}
	if len(f.released) != 1 || f.released[0] != f.content {
		t.Fatalf("released = %v, want [%#x]: the alloc/init content must be balanced", f.released, f.content)
	}
}

func TestPostGeneratesAnIdentifier(t *testing.T) {
	f := workingOS(t)
	id, err := Post(context.Background(), Notification{Title: "no id"})
	if err != nil {
		t.Fatalf("Post() = %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("Post() generated id = %q, want 32 hex characters", id)
	}
	if len(f.newRequestIn) != 1 || f.newRequestIn[0] != id {
		t.Fatalf("newRequest got %v, want the generated id %q", f.newRequestIn, id)
	}
}

func TestPostInvalid(t *testing.T) {
	workingOS(t)
	if _, err := Post(context.Background(), Notification{}); !errors.Is(err, ErrEmptyTitle) {
		t.Fatalf("Post(no title) = %v, want ErrEmptyTitle", err)
	}
}

func TestPostIdentifierGenerationFailure(t *testing.T) {
	workingOS(t)
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }

	if _, err := Post(context.Background(), Notification{Title: "t"}); err == nil ||
		!strings.Contains(err.Error(), "identifier") {
		t.Fatalf("Post() = %v, want an identifier-generation failure", err)
	}
}

func TestPostNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if _, err := Post(context.Background(), Notification{Title: "t"}); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("Post() = %v, want ErrNotBundled", err)
	}
}

func TestPostNilContent(t *testing.T) {
	f := workingOS(t)
	f.content = 0
	_, err := Post(context.Background(), Notification{Title: "t"})
	if !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("Post() = %v, want ErrObjectMissing", err)
	}
	if !strings.Contains(err.Error(), "UNMutableNotificationContent") {
		t.Fatalf("error %q does not name the missing object", err)
	}
	if len(f.released) != 0 {
		t.Fatalf("released %v; nothing was allocated, so nothing may be released", f.released)
	}
}

func TestPostNilRequest(t *testing.T) {
	f := workingOS(t)
	f.request = 0
	_, err := Post(context.Background(), Notification{Title: "t"})
	if !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("Post() = %v, want ErrObjectMissing", err)
	}
	if !strings.Contains(err.Error(), "UNNotificationRequest") {
		t.Fatalf("error %q does not name the missing object", err)
	}
	if len(f.released) != 1 {
		t.Fatalf("released = %v, want the content released even on the failure path", f.released)
	}
}

func TestPostSystemError(t *testing.T) {
	f := workingOS(t)
	f.addMsg = "Notifications are not allowed for this application"
	_, err := Post(context.Background(), Notification{Title: "t"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Post() = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error %q does not carry the system's own message", err)
	}
}

func TestPostContextDone(t *testing.T) {
	f := workingOS(t)
	f.silentAdd = true
	if _, err := Post(cancelled(), Notification{Title: "t"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Post() = %v, want context.Canceled", err)
	}
	if len(f.released) != 1 {
		t.Fatalf("released = %v, want the content released even when ctx expires", f.released)
	}
}

// ---------------------------------------------------------------------------
// Delivered / Pending.
// ---------------------------------------------------------------------------

func TestDeliveredAndPending(t *testing.T) {
	f := workingOS(t)
	f.recs = []Record{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}

	got, err := Delivered(context.Background())
	if err != nil {
		t.Fatalf("Delivered() = %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].Title != "B" {
		t.Fatalf("Delivered() = %+v", got)
	}
	if _, err := Pending(context.Background()); err != nil {
		t.Fatalf("Pending() = %v", err)
	}
	want := []bool{false, true}
	if len(f.pendingAsked) != 2 || f.pendingAsked[0] != want[0] || f.pendingAsked[1] != want[1] {
		t.Fatalf("pending flags = %v, want %v: Delivered and Pending must not be swapped", f.pendingAsked, want)
	}
}

func TestRecordsNilArray(t *testing.T) {
	f := workingOS(t)
	f.recsOK = false
	if _, err := Delivered(context.Background()); !errors.Is(err, ErrObjectMissing) {
		t.Fatalf("Delivered() = %v, want ErrObjectMissing", err)
	}
}

func TestRecordsContextDone(t *testing.T) {
	f := workingOS(t)
	f.silentRecs = true
	if _, err := Delivered(cancelled()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delivered() = %v, want context.Canceled", err)
	}
}

func TestRecordsNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if _, err := Pending(context.Background()); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("Pending() = %v, want ErrNotBundled", err)
	}
}

// ---------------------------------------------------------------------------
// Remove / RemoveAll.
// ---------------------------------------------------------------------------

func TestRemove(t *testing.T) {
	f := workingOS(t)
	if err := Remove(context.Background(), "a", "b"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if len(f.removedIDs) != 2 || f.removedIDs[0] != "a" || f.removedIDs[1] != "b" {
		t.Fatalf("removed = %v, want [a b]", f.removedIDs)
	}
}

// TestRemoveNothingIsANoOp pins that Remove() with no arguments does not reach
// the OS — and in particular does not become RemoveAll by accident.
func TestRemoveNothingIsANoOp(t *testing.T) {
	f := workingOS(t)
	f.bundleID = "" // would fail if it reached center()
	if err := Remove(context.Background()); err != nil {
		t.Fatalf("Remove() = %v, want nil", err)
	}
	if len(f.removedIDs) != 0 || f.removedAll != 0 {
		t.Fatalf("Remove() touched the OS: ids=%v all=%d", f.removedIDs, f.removedAll)
	}
}

func TestRemoveRejectsBadIdentifiers(t *testing.T) {
	for _, c := range []struct {
		name string
		ids  []string
		want error
	}{
		{"empty", []string{"a", ""}, ErrEmptyID},
		{"nul", []string{"a\x00b"}, ErrHasNUL},
		{"tooLong", []string{strings.Repeat("x", MaxIdentifierLen+1)}, ErrIDTooLong},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := workingOS(t)
			if err := Remove(context.Background(), c.ids...); !errors.Is(err, c.want) {
				t.Fatalf("Remove(%q) = %v, want %v", c.ids, err, c.want)
			}
			if len(f.removedIDs) != 0 {
				t.Fatalf("Remove sent %v to the OS despite invalid input", f.removedIDs)
			}
		})
	}
}

func TestRemoveNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if err := Remove(context.Background(), "a"); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("Remove() = %v, want ErrNotBundled", err)
	}
}

func TestRemoveAll(t *testing.T) {
	f := workingOS(t)
	if err := RemoveAll(context.Background()); err != nil {
		t.Fatalf("RemoveAll() = %v", err)
	}
	if f.removedAll != 1 {
		t.Fatalf("removeEverything called %d times, want 1", f.removedAll)
	}
	if len(f.pendingAsked) != 1 {
		t.Fatalf("the flush round trip did not happen: %v", f.pendingAsked)
	}
}

// TestRemoveFlushes pins the round trip that makes a removal real. Without it
// the one-way message dies with a process that exits promptly afterwards.
func TestRemoveFlushes(t *testing.T) {
	f := workingOS(t)
	if err := Remove(context.Background(), "a"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if len(f.pendingAsked) != 1 {
		t.Fatalf("Remove did not flush: %v", f.pendingAsked)
	}
}

// TestRemoveFlushTimeout covers the ctx branch of the flush.
func TestRemoveFlushTimeout(t *testing.T) {
	f := workingOS(t)
	f.silentRecs = true
	if err := Remove(cancelled(), "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove() = %v, want context.Canceled", err)
	}
	if len(f.removedIDs) != 1 {
		t.Fatalf("the removal itself must still have been sent: %v", f.removedIDs)
	}
}

func TestRemoveAllFlushTimeout(t *testing.T) {
	f := workingOS(t)
	f.silentRecs = true
	if err := RemoveAll(cancelled()); !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveAll() = %v, want context.Canceled", err)
	}
	if f.removedAll != 1 {
		t.Fatalf("the removal itself must still have been sent")
	}
}

func TestRemoveAllNotBundled(t *testing.T) {
	f := workingOS(t)
	f.bundleID = ""
	if err := RemoveAll(context.Background()); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("RemoveAll() = %v, want ErrNotBundled", err)
	}
}

// ---------------------------------------------------------------------------
// A completion handler really does arrive on another goroutine, so the
// synchronous wrappers must not assume it is called inline.
// ---------------------------------------------------------------------------

func TestPostToleratesAnAsynchronousCompletion(t *testing.T) {
	workingOS(t)
	addRequest = func(_ uintptr, _ uintptr, done func(string)) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			done("")
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Post(ctx, Notification{Title: "async"}); err != nil {
		t.Fatalf("Post() with an off-goroutine completion = %v", err)
	}
}
