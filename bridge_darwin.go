// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

//go:build darwin

package usernotifications

import (
	"sync"

	"github.com/go-macos/objc"
)

// Framework is the UserNotifications framework this package binds.
// github.com/go-macos/objc carries constants for the frameworks the fleet uses
// most; this one lives here, with its only consumer.
const Framework = "/System/Library/Frameworks/UserNotifications.framework/UserNotifications"

var (
	loadOnce sync.Once
	loadErr  error
)

// init points the seams declared in usernotifications.go at the real
// UserNotifications-backed implementations. Everything above them — the guards,
// the validation, the identifier generation and the context plumbing — is
// shared with every platform; only these leaf message sends are darwin-specific.
//
// This is github.com/go-macos/objc's own arrangement, and the reason for it is
// the same: what is above the seams can be tested anywhere, and what is below
// them is small enough to read.
func init() {
	loadFrameworks = func() error {
		loadOnce.Do(func() { loadErr = objc.Load(objc.Foundation, Framework) })
		return loadErr
	}

	bundleIdentifier = func() string {
		// -[NSBundle mainBundle] is never nil, but -bundleIdentifier is exactly
		// nil for an executable that is not inside an .app — which is the whole
		// point of asking. objc.GoString renders a nil object as "".
		return objc.GoString(objc.ClassID("NSBundle").
			Send(objc.Sel("mainBundle")).
			Send(objc.Sel("bundleIdentifier")))
	}

	// ⚠ Only ever reached after bundleIdentifier has answered non-empty. See
	// center() in usernotifications.go: outside a bundle this send does not
	// return nil, it throws NSInternalInconsistencyException, and an
	// Objective-C exception crossing a purego frame kills the process.
	currentCenter = func() uintptr {
		return uintptr(objc.ClassID("UNUserNotificationCenter").
			Send(objc.Sel("currentNotificationCenter")))
	}

	newContent = func(n Notification) uintptr {
		c := objc.ClassID("UNMutableNotificationContent").
			Send(objc.Sel("alloc")).Send(objc.Sel("init"))
		if c == 0 {
			return 0
		}
		c.Send(objc.Sel("setTitle:"), objc.NSString(n.Title))
		if n.Subtitle != "" {
			c.Send(objc.Sel("setSubtitle:"), objc.NSString(n.Subtitle))
		}
		if n.Body != "" {
			c.Send(objc.Sel("setBody:"), objc.NSString(n.Body))
		}
		switch n.Sound {
		case Silent:
			// Leave content.sound nil: that is what silence is.
		case DefaultSound:
			c.Send(objc.Sel("setSound:"), objc.ClassID("UNNotificationSound").
				Send(objc.Sel("defaultSound")))
		default:
			c.Send(objc.Sel("setSound:"), objc.ClassID("UNNotificationSound").
				Send(objc.Sel("soundNamed:"), objc.NSString(string(n.Sound))))
		}
		return uintptr(c)
	}

	newRequest = func(id string, content uintptr) uintptr {
		// A nil trigger means "deliver now", which is the only mode this
		// package offers; UNTimeIntervalNotificationTrigger and friends belong
		// to a scheduler, not to a notification binding.
		return uintptr(objc.ClassID("UNNotificationRequest").
			Send(objc.Sel("requestWithIdentifier:content:trigger:"),
				objc.NSString(id), objc.ID(content), objc.ID(0)))
	}

	releaseObject = func(obj uintptr) { objc.ID(obj).Send(objc.Sel("release")) }

	requestAuth = func(c uintptr, opts uint64, done func(bool, string)) {
		blk := objc.NewBlock(func(b objc.Block, granted bool, err objc.ID) {
			// Release through the block the runtime handed us, not through a
			// captured variable: the handler can run before NewBlock's result
			// has been assigned anywhere.
			defer b.Release()
			done(granted, errMessage(err))
		})
		objc.ID(c).Send(objc.Sel("requestAuthorizationWithOptions:completionHandler:"), opts, blk)
	}

	fetchSettings = func(c uintptr, done func(Settings, bool)) {
		blk := objc.NewBlock(func(b objc.Block, s objc.ID) {
			defer b.Release()
			if s == 0 {
				done(Settings{}, false)
				return
			}
			done(Settings{
				Status: Status(objc.Send[int64](s, objc.Sel("authorizationStatus"))),
				Alert:  Setting(objc.Send[int64](s, objc.Sel("alertSetting"))),
				Sound:  Setting(objc.Send[int64](s, objc.Sel("soundSetting"))),
				Badge:  Setting(objc.Send[int64](s, objc.Sel("badgeSetting"))),
			}, true)
		})
		objc.ID(c).Send(objc.Sel("getNotificationSettingsWithCompletionHandler:"), blk)
	}

	addRequest = func(c, request uintptr, done func(string)) {
		blk := objc.NewBlock(func(b objc.Block, err objc.ID) {
			defer b.Release()
			done(errMessage(err))
		})
		objc.ID(c).Send(objc.Sel("addNotificationRequest:withCompletionHandler:"),
			objc.ID(request), blk)
	}

	fetchRecords = func(c uintptr, pending bool, done func([]Record, bool)) {
		getter := objc.Sel("getDeliveredNotificationsWithCompletionHandler:")
		if pending {
			getter = objc.Sel("getPendingNotificationRequestsWithCompletionHandler:")
		}
		blk := objc.NewBlock(func(b objc.Block, arr objc.ID) {
			defer b.Release()
			if arr == 0 {
				done(nil, false)
				return
			}
			done(readRecords(arr, pending), true)
		})
		objc.ID(c).Send(getter, blk)
	}

	removeIdentifiers = func(c uintptr, ids []string) {
		arr := objc.ClassID("NSMutableArray").Send(objc.Sel("array"))
		for _, id := range ids {
			arr.Send(objc.Sel("addObject:"), objc.NSString(id))
		}
		// An identifier can be in either set; the caller asked for it to be
		// gone, so both are cleared. Neither call reports anything back.
		objc.ID(c).Send(objc.Sel("removeDeliveredNotificationsWithIdentifiers:"), arr)
		objc.ID(c).Send(objc.Sel("removePendingNotificationRequestsWithIdentifiers:"), arr)
	}

	removeEverything = func(c uintptr) {
		objc.ID(c).Send(objc.Sel("removeAllDeliveredNotifications"))
		objc.ID(c).Send(objc.Sel("removeAllPendingNotificationRequests"))
	}
}

// errMessage renders an NSError as a message, and NEVER renders a non-nil error
// as the empty string.
//
// The empty string is this package's signal for "no error", so an NSError whose
// -localizedDescription happened to be empty would be read as success by every
// caller. That is the same defect as a message to nil returning zero: a real
// failure arriving as a plausible-looking nothing.
func errMessage(err objc.ID) string {
	if err == 0 {
		return ""
	}
	if m := objc.GoString(err.Send(objc.Sel("localizedDescription"))); m != "" {
		return m
	}
	return "unknown error (the NSError had no localized description)"
}

// readRecords flattens an NSArray of notifications into Records.
//
// The two getters return different element types, and this is the trap in them:
// the pending getter yields UNNotificationRequest objects directly, while the
// delivered getter yields UNNotification objects that WRAP a request. Reading a
// delivered array as if it held requests gets nil from -identifier and -content
// for every element, and produces exactly the right number of completely empty
// records — a result that looks like a working call over an empty system.
func readRecords(arr objc.ID, pending bool) []Record {
	n := int(arr.Send(objc.Sel("count")))
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		req := arr.Send(objc.Sel("objectAtIndex:"), i)
		if !pending {
			req = req.Send(objc.Sel("request"))
		}
		content := req.Send(objc.Sel("content"))
		out = append(out, Record{
			ID:       objc.GoString(req.Send(objc.Sel("identifier"))),
			Title:    objc.GoString(content.Send(objc.Sel("title"))),
			Subtitle: objc.GoString(content.Send(objc.Sel("subtitle"))),
			Body:     objc.GoString(content.Send(objc.Sel("body"))),
		})
	}
	return out
}
