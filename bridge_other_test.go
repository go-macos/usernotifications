// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build !darwin

package usernotifications

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// These run on the non-darwin lanes and pin two things: that a consumer who
// cross-compiles gets one clean error rather than a missing symbol, and that
// the stubs themselves are executed rather than being dead weight nobody ever
// calls. Off darwin the whole library is the portable core plus these stubs, so
// there is no excuse for a single uncovered line.

// TestStubsAreReachable calls each stub directly. Everything that reaches the
// OS on darwin has a counterpart here, and it must answer rather than panic.
func TestStubsAreReachable(t *testing.T) {
	if err := loadFrameworks(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("loadFrameworks() = %v, want ErrUnsupported", err)
	}
	if got := bundleIdentifier(); got != "" {
		t.Errorf("bundleIdentifier() = %q, want \"\"", got)
	}
	if got := currentCenter(); got != 0 {
		t.Errorf("currentCenter() = %v, want 0", got)
	}
	if got := newContent(Notification{Title: "t"}); got != 0 {
		t.Errorf("newContent() = %v, want 0", got)
	}
	if got := newRequest("id", 1); got != 0 {
		t.Errorf("newRequest() = %v, want 0", got)
	}

	// The remaining stubs have nothing to return. Calling them proves they are
	// present and inert: a nil seam here would be a panic on a Linux consumer's
	// first call, which is exactly the failure the stubs exist to prevent.
	releaseObject(1)
	requestAuth(1, 0, func(bool, string) { t.Error("the stub invoked its completion handler") })
	fetchSettings(1, func(Settings, bool) { t.Error("the stub invoked its completion handler") })
	addRequest(1, 1, func(string) { t.Error("the stub invoked its completion handler") })
	fetchRecords(1, false, func([]Record, bool) { t.Error("the stub invoked its completion handler") })
	removeIdentifiers(1, []string{"a"})
	removeEverything(1)
}

// TestEveryEntryPointReportsUnsupported walks the whole public surface.
func TestEveryEntryPointReportsUnsupported(t *testing.T) {
	ctx := context.Background()

	if err := Available(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Available() = %v, want ErrUnsupported", err)
	}
	if got := BundleIdentifier(); got != "" {
		t.Errorf("BundleIdentifier() = %q, want \"\"", got)
	}
	if _, err := RequestAuthorization(ctx, OptionAlert); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RequestAuthorization() = %v, want ErrUnsupported", err)
	}
	if _, err := CurrentSettings(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CurrentSettings() = %v, want ErrUnsupported", err)
	}
	if _, err := Post(ctx, Notification{Title: "t"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Post() = %v, want ErrUnsupported", err)
	}
	if _, err := Delivered(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Delivered() = %v, want ErrUnsupported", err)
	}
	if _, err := Pending(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Pending() = %v, want ErrUnsupported", err)
	}
	if err := Remove(ctx, "a"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Remove() = %v, want ErrUnsupported", err)
	}
	if err := RemoveAll(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RemoveAll() = %v, want ErrUnsupported", err)
	}
}

// TestValidationStillAppliesOffDarwin pins that input is judged before the
// platform is: a caller developing on Linux still learns their notification is
// malformed, rather than being told only that macOS is elsewhere.
func TestValidationStillAppliesOffDarwin(t *testing.T) {
	ctx := context.Background()
	if _, err := Post(ctx, Notification{}); !errors.Is(err, ErrEmptyTitle) {
		t.Errorf("Post(no title) = %v, want ErrEmptyTitle", err)
	}
	if _, err := Post(ctx, Notification{Title: "a\x00b"}); !errors.Is(err, ErrHasNUL) {
		t.Errorf("Post(NUL) = %v, want ErrHasNUL", err)
	}
	if err := Remove(ctx, ""); !errors.Is(err, ErrEmptyID) {
		t.Errorf("Remove(empty) = %v, want ErrEmptyID", err)
	}
	if err := Remove(ctx, strings.Repeat("x", MaxIdentifierLen+1)); !errors.Is(err, ErrIDTooLong) {
		t.Errorf("Remove(long) = %v, want ErrIDTooLong", err)
	}
	// And Remove with nothing to remove stays a no-op on every platform.
	if err := Remove(ctx); err != nil {
		t.Errorf("Remove() = %v, want nil", err)
	}
}
