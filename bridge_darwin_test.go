// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build darwin

package usernotifications

import (
	"testing"

	"github.com/go-macos/objc"
)

// These tests drive the REAL bindings in bridge_darwin.go — the ones that can
// be driven from a bare test binary.
//
// That is a real line, not a convenience: every UserNotifications object except
// the center itself can be built outside an .app bundle, and only
// +[UNUserNotificationCenter currentNotificationCenter] throws there. So
// content, requests, sounds, NSError rendering and record flattening are all
// exercised here for real, and nothing in this file goes anywhere near the
// center. The parts that need one are in live_darwin_test.go, which builds an
// .app around cmd/unsend and runs it.

func TestRealLoadFrameworks(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}
	// Idempotent: the sync.Once means a second call is free and still nil.
	if err := loadFrameworks(); err != nil {
		t.Fatalf("second loadFrameworks() = %v", err)
	}
	if objc.GetClass("UNUserNotificationCenter") == 0 {
		t.Fatal("UserNotifications loaded but UNUserNotificationCenter is not a class")
	}
}

// TestRealBundleIdentifierIsEmptyInATestBinary pins the fact the whole package
// is built around: `go test` produces a bare executable, not an .app, so the
// guard fires and every entry point refuses. If this ever returns non-empty the
// test binary has been bundled and the live suite should be running instead.
func TestRealBundleIdentifierIsEmptyInATestBinary(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}
	if got := bundleIdentifier(); got != "" {
		t.Fatalf("bundleIdentifier() = %q in a test binary, want \"\"", got)
	}
	if err := Available(); err != ErrNotBundled {
		t.Fatalf("Available() = %v, want ErrNotBundled", err)
	}
}

// TestRealNewContent builds real UNMutableNotificationContent objects and reads
// the fields back out of the Objective-C object, so a setter that silently did
// nothing would be caught.
func TestRealNewContent(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}
	read := func(obj uintptr, sel string) string {
		return objc.GoString(objc.ID(obj).Send(objc.Sel(sel)))
	}

	t.Run("full", func(t *testing.T) {
		c := newContent(Notification{
			Title: "Title", Subtitle: "Subtitle", Body: "Body", Sound: DefaultSound,
		})
		if c == 0 {
			t.Fatal("newContent() = 0")
		}
		defer releaseObject(c)
		if got := read(c, "title"); got != "Title" {
			t.Errorf("title = %q", got)
		}
		if got := read(c, "subtitle"); got != "Subtitle" {
			t.Errorf("subtitle = %q", got)
		}
		if got := read(c, "body"); got != "Body" {
			t.Errorf("body = %q", got)
		}
		if objc.ID(c).Send(objc.Sel("sound")) == 0 {
			t.Error("sound is nil after DefaultSound")
		}
	})

	t.Run("silentLeavesSoundNil", func(t *testing.T) {
		c := newContent(Notification{Title: "T"})
		if c == 0 {
			t.Fatal("newContent() = 0")
		}
		defer releaseObject(c)
		if got := objc.ID(c).Send(objc.Sel("sound")); got != 0 {
			t.Errorf("sound = %v with Silent, want nil", got)
		}
		// Subtitle and Body were empty and must stay empty, not become "<nil>".
		if got := read(c, "subtitle"); got != "" {
			t.Errorf("subtitle = %q, want empty", got)
		}
		if got := read(c, "body"); got != "" {
			t.Errorf("body = %q, want empty", got)
		}
	})

	t.Run("namedSound", func(t *testing.T) {
		c := newContent(Notification{Title: "T", Sound: Sound("Submarine.aiff")})
		if c == 0 {
			t.Fatal("newContent() = 0")
		}
		defer releaseObject(c)
		if objc.ID(c).Send(objc.Sel("sound")) == 0 {
			t.Error("sound is nil after a named sound")
		}
	})
}

// TestRealNewRequest builds a real UNNotificationRequest and reads the
// identifier and content back through it — which is also what proves the
// request kept the content after this frame released its own reference.
func TestRealNewRequest(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}
	c := newContent(Notification{Title: "Kept", Body: "Also kept"})
	if c == 0 {
		t.Fatal("newContent() = 0")
	}
	r := newRequest("some-identifier", c)
	releaseObject(c) // exactly what Post does, and before reading anything back
	if r == 0 {
		t.Fatal("newRequest() = 0")
	}
	req := objc.ID(r)
	if got := objc.GoString(req.Send(objc.Sel("identifier"))); got != "some-identifier" {
		t.Errorf("identifier = %q", got)
	}
	content := req.Send(objc.Sel("content"))
	if content == 0 {
		t.Fatal("the request has no content")
	}
	if got := objc.GoString(content.Send(objc.Sel("title"))); got != "Kept" {
		t.Errorf("title through the request = %q, want %q", got, "Kept")
	}
	if got := req.Send(objc.Sel("trigger")); got != 0 {
		t.Errorf("trigger = %v, want nil (deliver now)", got)
	}
}

// nsError builds an NSError for errMessage to render.
func nsError(t *testing.T, domain string, code int, description string) objc.ID {
	t.Helper()
	info := objc.MapToDict(map[string]string{"NSLocalizedDescription": description})
	e := objc.ClassID("NSError").Send(objc.Sel("errorWithDomain:code:userInfo:"),
		objc.NSString(domain), code, info)
	if e == 0 {
		t.Fatal("could not build an NSError")
	}
	return e
}

// TestRealErrMessage covers the rendering of an NSError, and above all the
// branch that exists so a real failure can never arrive as the empty string —
// which is this package's signal for success.
func TestRealErrMessage(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}
	if got := errMessage(0); got != "" {
		t.Fatalf("errMessage(nil) = %q, want \"\"", got)
	}
	if got := errMessage(nsError(t, "UNErrorDomain", 1, "Notifications are not allowed")); got != "Notifications are not allowed" {
		t.Fatalf("errMessage() = %q", got)
	}
	// An NSError whose localized description is empty must NOT render as "".
	got := errMessage(nsError(t, "UNErrorDomain", 1, ""))
	if got == "" {
		t.Fatal("errMessage() rendered a non-nil NSError as the empty string, " +
			"which every caller reads as success")
	}
	if got != "unknown error (the NSError had no localized description)" {
		t.Logf("note: Foundation supplied its own description for an empty one: %q", got)
	}
}

// TestRealReadRecords drives the record flattener over a real NSArray.
//
// The pending shape (an array of UNNotificationRequest) is built here directly.
// The delivered shape (an array of UNNotification, each WRAPPING a request) can
// only come from a live center, so it is asserted in live_darwin_test.go.
func TestRealReadRecords(t *testing.T) {
	if err := loadFrameworks(); err != nil {
		t.Fatalf("loadFrameworks() = %v", err)
	}

	t.Run("empty", func(t *testing.T) {
		empty := objc.ClassID("NSArray").Send(objc.Sel("array"))
		for _, pending := range []bool{true, false} {
			if got := readRecords(empty, pending); len(got) != 0 {
				t.Errorf("readRecords(empty, %v) = %+v", pending, got)
			}
		}
	})

	t.Run("pending", func(t *testing.T) {
		arr := objc.ClassID("NSMutableArray").Send(objc.Sel("array"))
		for _, n := range []Notification{
			{ID: "one", Title: "One", Subtitle: "Sub one", Body: "Body one"},
			{ID: "two", Title: "Two"},
		} {
			c := newContent(n)
			r := newRequest(n.ID, c)
			releaseObject(c)
			arr.Send(objc.Sel("addObject:"), objc.ID(r))
		}
		got := readRecords(arr, true)
		want := []Record{
			{ID: "one", Title: "One", Subtitle: "Sub one", Body: "Body one"},
			{ID: "two", Title: "Two"},
		}
		if len(got) != len(want) {
			t.Fatalf("readRecords() = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}
