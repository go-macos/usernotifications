// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

// Command unsend posts and inspects macOS user notifications from the command
// line, through github.com/go-macos/usernotifications.
//
// It exists for two reasons. It is the smallest complete example of the
// library, and it is what the library's own live test bundles into an .app and
// runs — so the thing shipped to a user is the thing that is tested.
//
// Every subcommand prints a single JSON object on stdout and exits 0 on
// success, or prints {"error": "..."} and exits 1.
//
//	unsend check                     is this process able to post at all?
//	unsend auth [-provisional]       ask the user for permission
//	unsend settings                  what the system currently allows
//	unsend post -title T [-body B]   deliver a notification
//	unsend list [-pending]           what the system is holding
//	unsend remove ID...              withdraw identifiers
//	unsend remove-all                withdraw everything
//
// ⚠ It must be run from inside an .app bundle. Run the bare executable and
// every subcommand reports the "not bundled" error instead, because
// UNUserNotificationCenter cannot be reached without a bundle identifier. See
// the package README for the four-line recipe, or build one in Go with
// github.com/go-macos/appbundle.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	un "github.com/go-macos/usernotifications"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run is the whole program, parameterised on its arguments and output so it can
// be exercised without a process boundary.
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("unsend", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		title       = fs.String("title", "", "notification title (required by post)")
		subtitle    = fs.String("subtitle", "", "notification subtitle")
		body        = fs.String("body", "", "notification body")
		id          = fs.String("id", "", "notification identifier (generated when empty)")
		sound       = fs.String("sound", "", `sound: "" for silent, "default", or a file name such as Submarine.aiff`)
		provisional = fs.Bool("provisional", false, "ask for quiet provisional authorization: no dialog, delivered without a banner")
		pending     = fs.Bool("pending", false, "list pending rather than delivered notifications")
		timeout     = fs.Duration("timeout", 30*time.Second, "how long to wait for the system to answer")
	)
	fs.Usage = func() {
		fmt.Fprint(out, "usage: unsend <check|auth|settings|post|list|remove|remove-all> [flags]\n\n")
		fs.PrintDefaults()
	}
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	command, rest := args[0], args[1:]
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := dispatch(ctx, command, fs.Args(), options{
		notification: un.Notification{
			ID: *id, Title: *title, Subtitle: *subtitle, Body: *body, Sound: un.Sound(*sound),
		},
		provisional: *provisional,
		pending:     *pending,
	})
	if err != nil {
		emit(out, map[string]any{"error": err.Error(), "notBundled": errors.Is(err, un.ErrNotBundled)})
		return 1
	}
	emit(out, result)
	return 0
}

type options struct {
	notification un.Notification
	provisional  bool
	pending      bool
}

// dispatch runs one subcommand and returns what to print.
func dispatch(ctx context.Context, command string, rest []string, o options) (any, error) {
	switch command {
	case "check":
		err := un.Available()
		return map[string]any{
			"available":        err == nil,
			"bundleIdentifier": un.BundleIdentifier(),
			"reason":           errText(err),
		}, nil

	case "auth":
		opts := un.OptionAlert | un.OptionSound | un.OptionBadge
		if o.provisional {
			opts |= un.OptionProvisional
		}
		granted, err := un.RequestAuthorization(ctx, opts)
		if err != nil {
			return nil, err
		}
		return map[string]any{"granted": granted, "provisional": o.provisional}, nil

	case "settings":
		s, err := un.CurrentSettings(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status": s.Status.String(), "alert": s.Alert.String(),
			"sound": s.Sound.String(), "badge": s.Badge.String(),
		}, nil

	case "post":
		posted, err := un.Post(ctx, o.notification)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": posted}, nil

	case "list":
		list := un.Delivered
		if o.pending {
			list = un.Pending
		}
		recs, err := list(ctx)
		if err != nil {
			return nil, err
		}
		if recs == nil {
			recs = []un.Record{}
		}
		return map[string]any{"pending": o.pending, "count": len(recs), "notifications": recs}, nil

	case "remove":
		if len(rest) == 0 {
			return nil, errors.New("remove: no identifiers given (use remove-all to clear everything)")
		}
		if err := un.Remove(ctx, rest...); err != nil {
			return nil, err
		}
		return map[string]any{"removed": rest}, nil

	case "remove-all":
		if err := un.RemoveAll(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"removedAll": true}, nil
	}
	return nil, fmt.Errorf("unknown command %q", command)
}

// errText renders an error for a JSON field, and "" for no error.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// emit writes v as one line of JSON.
func emit(out io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(out, "{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintf(out, "%s\n", b)
}
