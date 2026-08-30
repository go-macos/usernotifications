// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package usernotifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Errors.
// ---------------------------------------------------------------------------

// Errors reported by the package. They are stable and may be tested with
// errors.Is.
var (
	// ErrUnsupported is reported by every entry point on non-darwin platforms.
	ErrUnsupported = errors.New("usernotifications: unsupported on this platform (darwin only)")

	// ErrNotBundled is reported when the process has no bundle identity, which
	// is the one condition that must be checked BEFORE the framework is
	// touched: +[UNUserNotificationCenter currentNotificationCenter] does not
	// fail politely outside an .app, it throws NSInternalInconsistencyException
	// and the process dies on SIGABRT. Build a bundle with
	// github.com/go-macos/appbundle.
	ErrNotBundled = errors.New("usernotifications: the process has no bundle identifier; UNUserNotificationCenter needs an .app bundle (see github.com/go-macos/appbundle)")

	// ErrNoCenter is reported when the process IS bundled but
	// currentNotificationCenter still came back nil — the framework failed to
	// load, or the class is not present on this system. It is deliberately
	// distinct from ErrNotBundled: they have different fixes.
	ErrNoCenter = errors.New("usernotifications: UNUserNotificationCenter is nil")

	// ErrObjectMissing is reported when the Objective-C runtime handed back a
	// nil object where a real one was required. A message to nil returns zero
	// in silence, so every object this package obtains is checked and named.
	ErrObjectMissing = errors.New("usernotifications: the Objective-C runtime returned a nil object")

	// ErrRejected wraps an NSError the system returned: not authorised, not
	// registered with LaunchServices, unsigned, and so on. The wrapped text is
	// the OS's own -localizedDescription.
	ErrRejected = errors.New("usernotifications: the system rejected the request")

	// ErrEmptyTitle is reported for a notification with no title. A title-less
	// notification is labelled only by the application name, which is a bug in
	// the caller far more often than it is a choice.
	ErrEmptyTitle = errors.New("usernotifications: empty title")

	// ErrHasNUL is reported for any field containing a NUL byte. Every string
	// crosses the bridge through +[NSString stringWithUTF8String:], which
	// terminates at the first NUL, so an embedded one would silently truncate
	// the field rather than fail.
	ErrHasNUL = errors.New("usernotifications: field contains a NUL byte")

	// ErrIDTooLong is reported for an identifier over MaxIdentifierLen bytes.
	ErrIDTooLong = errors.New("usernotifications: identifier too long")

	// ErrEmptyID is reported by Remove for an empty identifier. Empty is not a
	// wildcard: the system accepts a notification whose identifier is the empty
	// string, and then that one anonymous slot cannot be addressed apart from
	// any other. RemoveAll is how a caller says "all of them".
	ErrEmptyID = errors.New("usernotifications: empty identifier")
)

// MaxIdentifierLen bounds a notification identifier. Identifiers are short keys
// a program chooses for itself; this only rejects obviously malformed input
// before it reaches the framework.
const MaxIdentifierLen = 256

// ---------------------------------------------------------------------------
// Authorization.
// ---------------------------------------------------------------------------

// Options is a set of UNAuthorizationOptions: what a program asks permission to
// do when it calls [RequestAuthorization].
type Options uint64

// The authorization options worth asking for from a Go program. The values are
// UNAuthorizationOptions and must not be renumbered.
const (
	// OptionBadge asks to put a number on the dock tile.
	OptionBadge Options = 1 << 0
	// OptionSound asks to play a sound on delivery.
	OptionSound Options = 1 << 1
	// OptionAlert asks to display the notification. This is the one nearly
	// every caller means.
	OptionAlert Options = 1 << 2
	// OptionCriticalAlert asks to break through Do Not Disturb. It requires an
	// entitlement Apple grants by request; without it the request is refused.
	OptionCriticalAlert Options = 1 << 4
	// OptionProvisional asks for QUIET authorization: it is granted
	// immediately, with no dialog and no user interaction at all, and
	// notifications are delivered straight to Notification Center without a
	// banner until the user promotes or refuses them. It is the only option
	// that can be verified end-to-end on an unattended machine, which is what
	// this package's own live test uses. Combine it with OptionAlert and
	// OptionSound to say what the program would like once promoted.
	OptionProvisional Options = 1 << 6
)

// Status is a UNAuthorizationStatus: what the user has already decided.
type Status int

// The authorization states. The values are UNAuthorizationStatus and must not
// be renumbered.
const (
	// StatusNotDetermined means the user has never been asked.
	StatusNotDetermined Status = 0
	// StatusDenied means the user said no — including by dismissing the prompt
	// or by the asking process exiting before the prompt was answered.
	StatusDenied Status = 1
	// StatusAuthorized means the user said yes.
	StatusAuthorized Status = 2
	// StatusProvisional means OptionProvisional was granted quietly.
	StatusProvisional Status = 3
	// StatusEphemeral is an App Clip's temporary authorization.
	StatusEphemeral Status = 4
)

// String renders the status for a log line.
func (s Status) String() string {
	switch s {
	case StatusNotDetermined:
		return "not-determined"
	case StatusDenied:
		return "denied"
	case StatusAuthorized:
		return "authorized"
	case StatusProvisional:
		return "provisional"
	case StatusEphemeral:
		return "ephemeral"
	}
	return fmt.Sprintf("status(%d)", int(s))
}

// Setting is a UNNotificationSetting: whether one capability is switched on for
// this application in System Settings.
type Setting int

// The per-capability settings. The values are UNNotificationSetting and must
// not be renumbered.
const (
	// SettingNotSupported means the capability does not apply here.
	SettingNotSupported Setting = 0
	// SettingDisabled means the user switched it off.
	SettingDisabled Setting = 1
	// SettingEnabled means it is on.
	SettingEnabled Setting = 2
)

// String renders the setting for a log line.
func (s Setting) String() string {
	switch s {
	case SettingNotSupported:
		return "not-supported"
	case SettingDisabled:
		return "disabled"
	case SettingEnabled:
		return "enabled"
	}
	return fmt.Sprintf("setting(%d)", int(s))
}

// Settings is what the system currently allows this application, as reported by
// -getNotificationSettingsWithCompletionHandler:.
//
// Read Status and the individual settings together: an application can be
// StatusProvisional with Alert disabled and Sound enabled, which means its
// notifications reach Notification Center silently but never appear as a
// banner. A program that only checks Status will conclude it is working when
// nothing is visible.
type Settings struct {
	Status Status
	Alert  Setting
	Sound  Setting
	Badge  Setting
}

// ---------------------------------------------------------------------------
// The notification itself.
// ---------------------------------------------------------------------------

// Sound names the sound played when a notification is delivered.
//
// The zero value, [Silent], plays none. [DefaultSound] plays the system
// default. Any other value is the file name of a sound in the application
// bundle's Library/Sounds or in /Library/Sounds — and those carry an extension
// ("Submarine.aiff"), so a real file name can never collide with the reserved
// word DefaultSound.
//
// The framework is lenient here in a way worth knowing: +[UNNotificationSound
// soundNamed:] returns a usable object for a name that does not exist, and the
// system quietly falls back to the default at delivery time. A misspelt sound
// is therefore never reported as an error by anybody, here or in Objective-C.
type Sound string

// The two sounds that are not file names.
const (
	// Silent is the zero value: deliver without a sound.
	Silent Sound = ""
	// DefaultSound is the system's default notification sound.
	DefaultSound Sound = "default"
)

// Notification is what to show.
type Notification struct {
	// ID is the notification's identifier: the handle [Remove] takes, and the
	// key the system uses to REPLACE an already-delivered notification rather
	// than adding a second one. Leave it empty and [Post] generates a random
	// one and returns it.
	ID string
	// Title is the bold first line. It is required.
	Title string
	// Subtitle is an optional second line, shown smaller beside the title.
	Subtitle string
	// Body is the optional message text below them.
	Body string
	// Sound is what to play. The zero value is Silent.
	Sound Sound
}

// Record is a notification the system is holding: one already delivered, or one
// still pending. It carries what can be read back — the framework does not
// report the sound of a delivered notification, so [Notification] is not reused
// here rather than have a field that is always empty.
type Record struct {
	ID       string
	Title    string
	Subtitle string
	Body     string
}

// validate rejects a notification the bridge cannot carry faithfully.
func (n Notification) validate() error {
	if n.Title == "" {
		return ErrEmptyTitle
	}
	if len(n.ID) > MaxIdentifierLen {
		return fmt.Errorf("%w: %d bytes, max %d", ErrIDTooLong, len(n.ID), MaxIdentifierLen)
	}
	for name, v := range map[string]string{
		"ID": n.ID, "Title": n.Title, "Subtitle": n.Subtitle,
		"Body": n.Body, "Sound": string(n.Sound),
	} {
		if strings.IndexByte(v, 0) >= 0 {
			return fmt.Errorf("%w: %s", ErrHasNUL, name)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Seams. They are assigned in an init(): on darwin (bridge_darwin.go) to the
// real UserNotifications-backed implementations, elsewhere (bridge_other.go) to
// unsupported stubs.
//
// They are deliberately LEAF-shaped — each does one message send and nothing
// else — so that every guard, every error branch and all of the context and
// validation logic lives in this file, where it is reachable from a test on
// every platform without an .app bundle, a code signature or a window server.
// It is the same arrangement github.com/go-macos/objc uses in its own
// bridge_darwin.go init().
// ---------------------------------------------------------------------------

var (
	// loadFrameworks dlopens Foundation and UserNotifications, once.
	loadFrameworks func() error

	// bundleIdentifier returns -[[NSBundle mainBundle] bundleIdentifier], or ""
	// when the process is not in a bundle. This is the guard that stands
	// between a caller and an uncatchable Objective-C exception; nothing may
	// call currentCenter without it having answered non-empty first.
	bundleIdentifier func() string

	// currentCenter returns +[UNUserNotificationCenter currentNotificationCenter],
	// or 0 if it is nil.
	currentCenter func() uintptr

	// newContent builds a configured UNMutableNotificationContent (+1 retained,
	// released by the caller through releaseObject), or 0.
	newContent func(n Notification) uintptr

	// newRequest builds a UNNotificationRequest with no trigger, so the system
	// delivers it at once, or 0.
	newRequest func(id string, content uintptr) uintptr

	// releaseObject sends -release. It balances the alloc/init in newContent.
	releaseObject func(obj uintptr)

	// requestAuth calls -requestAuthorizationWithOptions:completionHandler:.
	// done receives an empty message on success.
	requestAuth func(center uintptr, opts uint64, done func(granted bool, errMsg string))

	// fetchSettings calls -getNotificationSettingsWithCompletionHandler:. ok is
	// false when the framework handed back a nil settings object.
	fetchSettings func(center uintptr, done func(s Settings, ok bool))

	// addRequest calls -addNotificationRequest:withCompletionHandler:. done
	// receives an empty message on success.
	addRequest func(center, request uintptr, done func(errMsg string))

	// fetchRecords calls -getDeliveredNotificationsWithCompletionHandler: or,
	// when pending is true, -getPendingNotificationRequestsWithCompletionHandler:.
	fetchRecords func(center uintptr, pending bool, done func(recs []Record, ok bool))

	// removeIdentifiers removes ids from BOTH the delivered and the pending
	// sets. Neither underlying method reports anything back.
	removeIdentifiers func(center uintptr, ids []string)

	// removeEverything empties both sets.
	removeEverything func(center uintptr)

	// randRead fills b with random bytes. A seam so the identifier generator's
	// failure branch is reachable.
	randRead = rand.Read
)

// center performs the whole obtain-and-guard sequence, in the one order that is
// safe, and is the only place in the package that reaches for the center.
//
//  1. Load the frameworks, or there is no class to find.
//  2. Ask for a bundle identifier. NO identifier means NO call to
//     currentNotificationCenter — that call throws outside a bundle and takes
//     the process with it.
//  3. Only then ask for the center, and check it: a message to nil returns zero
//     in silence, and a nil center would make every later send a no-op that
//     reports success.
func center() (uintptr, error) {
	if err := loadFrameworks(); err != nil {
		return 0, err
	}
	if bundleIdentifier() == "" {
		return 0, ErrNotBundled
	}
	c := currentCenter()
	if c == 0 {
		return 0, ErrNoCenter
	}
	return c, nil
}

// newIdentifier returns a fresh random identifier for a notification that did
// not name itself.
func newIdentifier() (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("usernotifications: generating an identifier: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------------------
// Public API.
// ---------------------------------------------------------------------------

// Available reports whether this process can use UNUserNotificationCenter at
// all, returning nil when it can.
//
// Call it early. It is the difference between a program that says "notifications
// need me to be inside an .app bundle" and one that dies on SIGABRT inside a
// framework, and it costs one message send. On [ErrNotBundled] a program can
// fall back to github.com/go-macos/notify, tell the user, or arrange to be
// bundled with github.com/go-macos/appbundle.
func Available() error {
	_, err := center()
	return err
}

// BundleIdentifier returns the identifier of the .app bundle this process is
// running inside, or "" if it is not in one.
//
// It is the exact question [Available] asks first, exposed on its own so a
// program can report WHICH application the user will be granting permission to
// — the name that appears in System Settings ▸ Notifications is chosen by this
// string, not by the executable's name.
func BundleIdentifier() string {
	if err := loadFrameworks(); err != nil {
		return ""
	}
	return bundleIdentifier()
}

// RequestAuthorization asks the user for permission to post notifications and
// reports whether it was granted.
//
// It blocks until the user answers, so pass a ctx with a deadline and expect to
// wait: opts without [OptionProvisional] puts a system dialog on screen and the
// completion handler does not run until somebody clicks it. If ctx expires
// first this returns ctx.Err() — and be aware that the OS records an unanswered
// prompt as a REFUSAL, so a program that gives up too early has permanently
// denied itself until the user goes to System Settings. Ask once, generously,
// at a moment the user is present.
//
// With [OptionProvisional] there is no dialog at all: authorization is granted
// immediately and quietly, and notifications go to Notification Center without
// a banner. That is the option to use on an unattended machine.
//
// A false return with a nil error is a plain refusal, not a malfunction.
func RequestAuthorization(ctx context.Context, opts Options) (bool, error) {
	c, err := center()
	if err != nil {
		return false, err
	}
	type answer struct {
		granted bool
		msg     string
	}
	ch := make(chan answer, 1)
	requestAuth(c, uint64(opts), func(granted bool, msg string) {
		ch <- answer{granted, msg}
	})
	select {
	case a := <-ch:
		if a.msg != "" {
			return a.granted, fmt.Errorf("%w: %s", ErrRejected, a.msg)
		}
		return a.granted, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// CurrentSettings reports what the system currently allows this application,
// without asking the user anything.
//
// Use it before [RequestAuthorization] to avoid re-asking somebody who has
// already answered, and after [Post] to explain why nothing appeared: see
// [Settings] for the combination that delivers silently.
func CurrentSettings(ctx context.Context) (Settings, error) {
	c, err := center()
	if err != nil {
		return Settings{}, err
	}
	type answer struct {
		s  Settings
		ok bool
	}
	ch := make(chan answer, 1)
	fetchSettings(c, func(s Settings, ok bool) { ch <- answer{s, ok} })
	select {
	case a := <-ch:
		if !a.ok {
			return Settings{}, fmt.Errorf("%w: UNNotificationSettings", ErrObjectMissing)
		}
		return a.s, nil
	case <-ctx.Done():
		return Settings{}, ctx.Err()
	}
}

// Post delivers n immediately and returns the identifier it was delivered
// under — n.ID, or a generated one if n.ID was empty.
//
// Posting a notification whose identifier matches one already delivered
// REPLACES it in place rather than adding a second. That is the framework's
// behaviour and it is the useful one: a progress or status notification should
// keep the same ID.
//
// Post succeeding means the system accepted the request, not that the user saw
// anything. An application whose alert setting is disabled, or which holds only
// provisional authorization, gets a nil error and a silent delivery to
// Notification Center. Check [CurrentSettings] when that matters, and
// [Delivered] when you need proof it landed.
func Post(ctx context.Context, n Notification) (string, error) {
	if err := n.validate(); err != nil {
		return "", err
	}
	id := n.ID
	if id == "" {
		var err error
		if id, err = newIdentifier(); err != nil {
			return "", err
		}
	}
	c, err := center()
	if err != nil {
		return "", err
	}

	content := newContent(n)
	if content == 0 {
		return "", fmt.Errorf("%w: UNMutableNotificationContent", ErrObjectMissing)
	}
	// newContent hands back an alloc/init object, owned by this frame. The
	// request copies what it needs, so the content is released either way.
	defer releaseObject(content)

	request := newRequest(id, content)
	if request == 0 {
		return "", fmt.Errorf("%w: UNNotificationRequest", ErrObjectMissing)
	}

	ch := make(chan string, 1)
	addRequest(c, request, func(msg string) { ch <- msg })
	select {
	case msg := <-ch:
		if msg != "" {
			return "", fmt.Errorf("%w: %s", ErrRejected, msg)
		}
		return id, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Delivered returns the notifications from this application that are currently
// in Notification Center.
//
// It is the only way to prove a [Post] actually landed: Post reports that the
// system accepted the request, whereas this reports what the system is holding.
func Delivered(ctx context.Context) ([]Record, error) {
	return records(ctx, false)
}

// Pending returns the notification requests this application has scheduled that
// have not been delivered yet.
//
// This package posts everything with no trigger, so its own notifications are
// never pending; the list is for requests made by other code in the same
// application.
func Pending(ctx context.Context) ([]Record, error) {
	return records(ctx, true)
}

// records is the shared body of Delivered and Pending.
func records(ctx context.Context, pending bool) ([]Record, error) {
	c, err := center()
	if err != nil {
		return nil, err
	}
	type answer struct {
		recs []Record
		ok   bool
	}
	ch := make(chan answer, 1)
	fetchRecords(c, pending, func(recs []Record, ok bool) { ch <- answer{recs, ok} })
	select {
	case a := <-ch:
		if !a.ok {
			return nil, fmt.Errorf("%w: NSArray of notifications", ErrObjectMissing)
		}
		return a.recs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Remove withdraws the given identifiers from both the delivered and the
// pending sets, so an identifier is gone from the system whichever state it was
// in. It returns once the notification daemon has the request.
//
// # Why this takes a context when the framework offers no completion handler
//
// -removeDeliveredNotificationsWithIdentifiers: and its pending twin return
// void and call nothing back: they are one-way messages posted to the daemon.
// A program that sends one and then exits therefore has NO guarantee the
// removal ever happened, and measurably often it does not — a command-line tool
// that removed a notification and returned left it on screen perhaps half the
// time, while the identical call from a process that stayed alive took effect
// at once.
//
// So this function does one synchronous round trip after the removal, on the
// same connection, purely to flush it. That is what ctx is for, and it is the
// difference between a removal and a hope.
//
// It confirms the daemon RECEIVED the request; it does not wait for the
// notification to disappear from Notification Center, which happens shortly
// afterwards. Poll [Delivered] if you need to observe that.
//
// An empty identifier is rejected rather than treated as a wildcard. The system
// really will accept a notification whose identifier is the empty string, and
// that one anonymous slot is then indistinguishable from any other; use
// [RemoveAll] to mean all of them. Calling Remove with no identifiers at all is
// a no-op, not an error.
func Remove(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			return ErrEmptyID
		}
		if strings.IndexByte(id, 0) >= 0 {
			return fmt.Errorf("%w: identifier", ErrHasNUL)
		}
		if len(id) > MaxIdentifierLen {
			return fmt.Errorf("%w: %d bytes, max %d", ErrIDTooLong, len(id), MaxIdentifierLen)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	c, err := center()
	if err != nil {
		return err
	}
	removeIdentifiers(c, ids)
	return flush(ctx, c)
}

// RemoveAll withdraws every notification this application has delivered or
// scheduled, and like [Remove] returns once the daemon has the request.
func RemoveAll(ctx context.Context) error {
	c, err := center()
	if err != nil {
		return err
	}
	removeEverything(c)
	return flush(ctx, c)
}

// flush issues one round trip to the notification daemon and discards the
// answer.
//
// Its only purpose is ordering. The removal methods are one-way, and a reply on
// the same connection cannot arrive before the messages queued ahead of it have
// been delivered — so once this returns, the removal is the daemon's problem
// rather than a message that dies with the process.
func flush(ctx context.Context, c uintptr) error {
	done := make(chan struct{}, 1)
	fetchRecords(c, false, func([]Record, bool) { done <- struct{}{} })
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
