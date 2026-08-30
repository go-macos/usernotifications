// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build !darwin

package usernotifications

// Framework is the path this package would bind on darwin. It is declared here
// too so a consumer that names it compiles everywhere.
const Framework = "/System/Library/Frameworks/UserNotifications.framework/UserNotifications"

// init points the seams at stubs. loadFrameworks is the first thing every
// entry point calls, so ErrUnsupported is what a consumer sees on every path;
// the rest exist so that no seam is nil if one is ever reached, and so the
// stubs themselves are covered rather than being dead weight nobody executes.
//
// Everything OS-independent — validation, identifier generation, the guards in
// center(), the context plumbing — stays fully functional and tested here,
// which is what makes this package's error handling verifiable on a Linux
// runner.
func init() {
	loadFrameworks = func() error { return ErrUnsupported }
	bundleIdentifier = func() string { return "" }
	currentCenter = func() uintptr { return 0 }
	newContent = func(Notification) uintptr { return 0 }
	newRequest = func(string, uintptr) uintptr { return 0 }
	releaseObject = func(uintptr) {}
	requestAuth = func(uintptr, uint64, func(bool, string)) {}
	fetchSettings = func(uintptr, func(Settings, bool)) {}
	addRequest = func(uintptr, uintptr, func(string)) {}
	fetchRecords = func(uintptr, bool, func([]Record, bool)) {}
	removeIdentifiers = func(uintptr, []string) {}
	removeEverything = func(uintptr) {}
}
