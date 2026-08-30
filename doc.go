// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Package usernotifications is a pure-Go (CGO_ENABLED=0) binding to
// UNUserNotificationCenter — the framework macOS actually uses to put a
// notification on screen and in Notification Center.
//
// It reaches the OS through github.com/go-macos/objc, which reaches it through
// github.com/ebitengine/purego: no cgo, no Objective-C source file in the
// build, and no shelling out to osascript.
//
// # Why this package exists at all
//
// github.com/go-macos/notify drives NSUserNotification, which Apple deprecated
// in macOS 11 and has since removed from the supported path. Its own
// PostUserNotification documents the deprecation honestly and declines to wrap
// the replacement. This package is the replacement, wrapped.
//
// Use this package for a user-facing notification. Keep using go-macos/notify
// for the two mechanisms that are not user-facing at all and that this package
// does not duplicate: the Darwin notify(3) event bus, and
// NSDistributedNotificationCenter.
//
// # The bundle requirement is not a warning, it is a crash
//
// UNUserNotificationCenter requires the process to be inside an .app bundle
// carrying a bundle identifier. Outside one it does not return nil and it does
// not return an error:
//
//	+[UNUserNotificationCenter currentNotificationCenter]
//	*** Terminating app due to uncaught exception 'NSInternalInconsistencyException',
//	    reason: 'bundleProxyForCurrentProcess is nil: mainBundle.bundleURL file:///…/'
//	SIGABRT: abort
//
// That is a measured transcript from a bare command-line binary on macOS 15,
// not a paraphrase of the documentation. An Objective-C exception crossing a
// purego frame has nowhere to be caught, so the process dies. There is no
// recovering from it after the fact and no way to ask the class to be gentler.
//
// So this package NEVER sends currentNotificationCenter without first asking
// -[[NSBundle mainBundle] bundleIdentifier] whether there is a bundle identity
// to speak for. Without one every entry point reports [ErrNotBundled] and
// touches nothing. [Available] asks the question on its own so a program can
// degrade deliberately — fall back to go-macos/notify, or tell the user — long
// before it wants to post anything.
//
// Assemble the bundle with github.com/go-macos/appbundle, which is the fleet's
// pure-Go .app writer:
//
//	_, err := appbundle.Build(appbundle.Spec{
//		Dir: "dist", Name: "myapp", Identifier: "io.github.example.myapp",
//		Version: "1.0.0", Executable: "build/myapp", MinimumSystem: "11.0",
//	})
//
// A bundle identifier is necessary but not sufficient: see the README for the
// two further conditions the system imposes — a code signature, and
// registration with LaunchServices — both of which this package reports as
// [ErrRejected] rather than guessing at.
//
// # Completion handlers are blocks, and blocks do work here
//
// Every asynchronous entry point in UserNotifications takes an Objective-C
// block, not a function pointer. go-macos/notify's own documentation records
// that a block "cannot be synthesised from Go under CGO_ENABLED=0 without
// hand-assembling the block ABI", and routed around them. That is no longer
// true: go-macos/objc grew [objc.NewBlock] in v0.3.0, which assembles exactly
// that ABI, and this package uses it directly. The completion handlers arrive
// on one of libdispatch's own threads, so — unlike
// NSDistributedNotificationCenter — nothing here needs a run loop, an
// NSApplication, or a locked OS thread.
//
// The blocks are therefore an implementation detail, and the API above them is
// synchronous and context-aware: [RequestAuthorization], [Post], [Delivered]
// and [CurrentSettings] block until the OS answers or ctx is done. That is the
// shape a caller wants, and it is honest, because there is a real answer to
// wait for in every case.
//
// # Removal has no completion handler
//
// [Remove] and [RemoveAll] take no context and confirm nothing, because the
// framework's removal methods return void and call nothing back. They report
// only what can be known synchronously: bad input, or no usable center. A
// caller that must observe the effect polls [Delivered].
//
// # Portability
//
// Every exported symbol is defined on all platforms so consumers
// cross-compile. On non-darwin GOOS every entry point reports
// [ErrUnsupported]; the OS-independent half — validation, identifier
// generation, the context plumbing and every guard — stays functional and
// tested there.
package usernotifications
