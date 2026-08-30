// Copyright (c) 2026, the go-macos authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// These cover the tool's argument handling on every platform. What the tool
// does with a real notification centre is the subject of the library's live
// suite, which bundles this very binary into an .app and drives it.

func TestRunNoArgumentsPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code != 2 {
		t.Fatalf("run(nil) = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Fatalf("no usage printed: %q", out.String())
	}
}

func TestRunBadFlag(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"post", "-nosuchflag"}, &out); code != 2 {
		t.Fatalf("run(bad flag) = %d, want 2", code)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"frobnicate"}, &out); code != 1 {
		t.Fatalf("run(unknown) = %d, want 1", code)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output %q is not JSON: %v", out.String(), err)
	}
	if msg, _ := m["error"].(string); !strings.Contains(msg, "frobnicate") {
		t.Fatalf("error %q does not name the unknown command", msg)
	}
}

// TestRunRemoveWithoutIdentifiers pins that "remove" with nothing to remove is
// an error rather than quietly becoming remove-all.
func TestRunRemoveWithoutIdentifiers(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"remove"}, &out); code != 1 {
		t.Fatalf("run(remove) = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "remove-all") {
		t.Fatalf("the error should point at remove-all: %q", out.String())
	}
}

// TestEmitUnmarshalableValue covers the branch where a result cannot be
// rendered as JSON, so the tool still says something instead of printing
// nothing at all.
func TestEmitUnmarshalableValue(t *testing.T) {
	var out bytes.Buffer
	emit(&out, map[string]any{"bad": make(chan int)})
	if !strings.Contains(out.String(), "error") {
		t.Fatalf("emit() printed %q, want an error object", out.String())
	}
}
