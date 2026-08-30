# go-macos/usernotifications

[![ci](https://github.com/go-macos/usernotifications/actions/workflows/ci.yml/badge.svg)](https://github.com/go-macos/usernotifications/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-macos/usernotifications.svg)](https://pkg.go.dev/github.com/go-macos/usernotifications)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

**macOS user notifications through `UNUserNotificationCenter` — the framework
the system actually uses — from pure Go, `CGO_ENABLED=0`.** No cgo, no
`osascript`, no Objective-C source file in the build: it reaches
UserNotifications through [`go-macos/objc`](https://github.com/go-macos/objc),
which reaches it through [purego](https://github.com/ebitengine/purego).

```go
if err := usernotifications.Available(); err != nil {
        return err // not in an .app bundle: say so, do not push on
}

granted, err := usernotifications.RequestAuthorization(ctx,
        usernotifications.OptionAlert|usernotifications.OptionSound)
if err != nil || !granted {
        return err
}

id, err := usernotifications.Post(ctx, usernotifications.Notification{
        Title: "Build finished",
        Body:  "12 packages, 0 failures",
        Sound: usernotifications.DefaultSound,
})
...
_ = usernotifications.Remove(ctx, id)
```

| | |
|---|---|
| `Available() error` | can this process post at all? Ask first. |
| `BundleIdentifier() string` | which application the user is granting permission to. |
| `RequestAuthorization(ctx, Options) (bool, error)` | ask the user. |
| `CurrentSettings(ctx) (Settings, error)` | what the system allows right now. |
| `Post(ctx, Notification) (string, error)` | deliver, returning the identifier. |
| `Delivered(ctx) ([]Record, error)` | what is in Notification Center. |
| `Pending(ctx) ([]Record, error)` | what is scheduled and not yet delivered. |
| `Remove(ctx, ids...) error` | withdraw identifiers, delivered or pending. |
| `RemoveAll(ctx) error` | withdraw everything. |

## Why not `go-macos/notify`?

[`go-macos/notify`](https://github.com/go-macos/notify) drives
`NSUserNotification`, **deprecated in macOS 11** and gone from the supported
path. Its own `PostUserNotification` says so and declines to wrap the
replacement. This package is that replacement.

Use `go-macos/notify` still for the two things it does that are not user-facing
and that this package does not duplicate: the Darwin `notify(3)` event bus, and
`NSDistributedNotificationCenter`.

## The bundle requirement is a crash, not a warning

This is the whole reason the package is shaped the way it is. Outside an `.app`
bundle, `+[UNUserNotificationCenter currentNotificationCenter]` does not return
`nil` and does not return an error:

```
*** Terminating app due to uncaught exception 'NSInternalInconsistencyException',
    reason: 'bundleProxyForCurrentProcess is nil: mainBundle.bundleURL file:///…/'
SIGABRT: abort
```

That transcript is from a bare command-line binary on macOS 15. An Objective-C
exception crossing a purego frame has nowhere to be caught, so the process dies
— there is no recovering from it afterwards.

So this package **never** asks for the center without first asking
`-[[NSBundle mainBundle] bundleIdentifier]` whether there is an identity to
speak for. Without one, every entry point returns `ErrNotBundled` and touches
nothing. That guard has its own test, and that test does not skip.

Build the bundle with
[`go-macos/appbundle`](https://github.com/go-macos/appbundle):

```go
_, err := appbundle.Build(appbundle.Spec{
        Dir: "dist", Name: "myapp", Identifier: "io.github.example.myapp",
        Version: "1.0.0", Executable: "build/myapp", MinimumSystem: "11.0",
})
```

### Three conditions, three different failures

A bundle identifier is necessary but not sufficient. Each of these was found by
measurement, and each fails differently:

| condition | what happens without it |
|---|---|
| an `.app` with a bundle identifier | `NSInternalInconsistencyException`, **SIGABRT** |
| a code signature, even ad-hoc | `UNErrorDomain` error 1, *"Notifications are not allowed for this application"* |
| registration with LaunchServices | the same error 1, indistinguishable from the above |

There is a fourth, which cost an hour: **an `.app` under `/var/folders`** — which
is what `t.TempDir()` gives you — is registered by `lsregister` without
complaint and then refused by the notification system, while the byte-identical
bundle under `~/Library/Application Support` is granted immediately.

The recipe, once:

```sh
go build -o MyApp.app/Contents/MacOS/myapp .
# ... write Contents/Info.plist with CFBundleIdentifier ...
codesign --force --sign - MyApp.app
/System/Library/Frameworks/CoreServices.framework/Frameworks/\
LaunchServices.framework/Support/lsregister -f MyApp.app
./MyApp.app/Contents/MacOS/myapp
```

## Completion handlers are blocks, and blocks work here

Every asynchronous call in UserNotifications takes an Objective-C **block**.
`go-macos/notify` recorded that a block "cannot be synthesised from Go under
`CGO_ENABLED=0` without hand-assembling the block ABI", and routed around them.
That is no longer true: `go-macos/objc` gained `NewBlock` in v0.3.0, which
assembles exactly that ABI, and this package uses it directly.

Two consequences worth knowing:

- **Nothing here needs a run loop.** The completion handlers arrive on
  libdispatch's own threads, so — unlike `NSDistributedNotificationCenter` —
  there is no `Run`, no `NSApplication` and no locked OS thread.
- **A block per call is fine.** purego caches the generated callback by function
  *type*, so the 2000-callback ceiling is charged once per signature, not once
  per notification. Each block is released from inside its own handler.

The blocks are an implementation detail. The API above them is synchronous and
takes a `context.Context`, because in every case there is a real answer to wait
for.

## `Remove` takes a context, and that is not decoration

`-removeDeliveredNotificationsWithIdentifiers:` and its siblings return `void`
and call nothing back: they are one-way messages. A program that sends one and
exits therefore has **no guarantee the removal happened**, and measurably often
it did not — the command-line tool in `cmd/unsend` left notifications on screen
about half the time, while the identical call from a process that stayed alive
took effect at once.

So `Remove` and `RemoveAll` make one synchronous round trip afterwards, on the
same connection, purely to flush the removal. That is what the context is for.
Fixing this took the live suite from 32 seconds of polling and flaky failures to
1.8 seconds and deterministic passes.

## Asking for permission, without a person present

`RequestAuthorization` without `OptionProvisional` puts a **modal dialog** on
somebody's screen, and the completion handler does not run until it is answered.
Worse, the system records an unanswered prompt as a permanent **refusal**: a
program that gives up early has denied itself until the user goes to System
Settings. Ask once, generously, when a person is there.

`OptionProvisional` is granted immediately with no dialog at all, and delivers
quietly to Notification Center without a banner. It is the only way to verify
the whole path on an unattended machine, and it is what this package's own live
test uses.

## The tool

`cmd/unsend` is the smallest complete example, and it is what the live test
bundles and drives — so the thing shipped is the thing tested.

```sh
unsend check                      # is this process able to post at all?
unsend auth -provisional          # quiet authorization, no dialog
unsend post -title Hi -body There
unsend list
unsend remove <id>
```

Every subcommand prints one JSON object.

## Testing

The bindings are split so that everything above the OS is testable anywhere:
the leaf message sends are package-level seams assigned in an `init()`, exactly
as [`go-macos/objc`](https://github.com/go-macos/objc) does in its own
`bridge_darwin.go`. Everything above them — the guards, the validation, the
identifier generation, the context plumbing — is portable and reaches **100% of
statements on every platform, error branches included**.

Below the seams there are three layers of real verification:

- the objects that *can* be built outside a bundle — content, requests, sounds,
  `NSError` rendering, record flattening — are exercised for real in a plain
  `go test`;
- the rest is driven by a live suite that assembles an `.app`, signs it,
  registers it and runs the real thing, asserting against what the system says
  it is holding rather than against a nil error;
- that subprocess is built with `-cover`, so its coverage is *measured* and not
  merely asserted.

The only statements not covered anywhere are three defensive nil-guards that a
working macOS never triggers.

BSD-3-Clause.
