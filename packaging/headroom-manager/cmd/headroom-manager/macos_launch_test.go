package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMacOSLaunchPayloadPreservesPrivateConfiguration(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Headroom test ü.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", "headroom")
	arguments := []string{"--config", filepath.Join(t.TempDir(), "settings with spaces.json"), "--headroom-ready-file", "quote'\"$()\nready.json"}
	environment := []string{
		"HEADROOM_INSTALL_ROOT=/a root with spaces",
		"HEADROOM_LAUNCHER_PATH=/Applications/Headroom.app/Contents/MacOS/headroom",
		"HEADROOM_PACKAGE_VERSION=2.0.1",
		"HEADROOM_READY_NONCE=private-ready-nonce",
		"USAGE_AUTH_TOKEN=private-test-value=with-equals",
		"QT_QPA_PLATFORM=cocoa",
		"__CFBundleIdentifier=io.headroom.launcher",
		"XPC_SERVICE_NAME=application.io.headroom.launcher",
	}
	payload, err := macOSLaunchPayload(executable, arguments, environment)
	if err != nil {
		t.Fatal(err)
	}
	var got macOSLaunchRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Bundle != bundle || !reflect.DeepEqual(got.Arguments, arguments) {
		t.Fatalf("launch target or arguments changed: %#v", got)
	}
	expected := map[string]string{
		"HEADROOM_INSTALL_ROOT":    "/a root with spaces",
		"HEADROOM_LAUNCHER_PATH":   "/Applications/Headroom.app/Contents/MacOS/headroom",
		"HEADROOM_PACKAGE_VERSION": "2.0.1",
		"HEADROOM_READY_NONCE":     "private-ready-nonce",
		"USAGE_AUTH_TOKEN":         "private-test-value=with-equals",
		"QT_QPA_PLATFORM":          "cocoa",
	}
	if !reflect.DeepEqual(got.Environment, expected) {
		t.Fatal("launch environment lost configuration or retained the wrapper identity")
	}
}

func TestMacOSLaunchPayloadAcceptsNoArguments(t *testing.T) {
	payload, err := macOSLaunchPayload(filepath.Join(t.TempDir(), "Headroom.app", "Contents", "MacOS", "headroom"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Arguments   any `json:"arguments"`
		Environment any `json:"environment"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Arguments.([]any); !ok {
		t.Fatal("no arguments must encode as an array, not null")
	}
	if _, ok := got.Environment.(map[string]any); !ok {
		t.Fatal("empty environment must encode as an object, not null")
	}
}

func TestMacOSLaunchPayloadRejectsNonBundleExecutable(t *testing.T) {
	for _, path := range []string{"headroom", filepath.Join(t.TempDir(), "headroom"), filepath.Join(t.TempDir(), "Headroom", "Contents", "MacOS", "headroom"), filepath.Join(t.TempDir(), "Headroom.app", "Resources", "MacOS", "headroom")} {
		if _, err := macOSLaunchPayload(path, nil, nil); err == nil {
			t.Fatalf("accepted non-bundle target %q", path)
		}
	}
}

func TestMacOSBackgroundLaunchPreventsQtFocusStealing(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "Headroom.app", "Contents", "MacOS", "headroom")
	for _, background := range []bool{false, true} {
		var arguments []string
		if background {
			arguments = []string{"--background"}
		}
		payload, err := macOSLaunchPayload(executable, arguments, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got macOSLaunchRequest
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatal(err)
		}
		value, present := got.Environment["QT_MAC_DISABLE_FOREGROUND_APPLICATION_TRANSFORM"]
		if background && value != "1" {
			t.Fatal("background launch permits Qt to activate itself")
		}
		if !background && present {
			t.Fatal("normal launch changed Qt activation policy")
		}
	}
}

func TestMacOSLaunchRoutesCocoaAndHeadlessBackends(t *testing.T) {
	for _, test := range []struct {
		name     string
		gui      bool
		platform string
		appKit   bool
	}{
		{"CLI", false, "cocoa", false},
		{"default GUI", true, "", true},
		{"Cocoa", true, "cocoa", true},
		{"Cocoa options", true, "cocoa:fontengine=freetype", true},
		{"Cocoa with fallback", true, "cocoa;offscreen", true},
		{"offscreen", true, "offscreen", false},
		{"offscreen options", true, "offscreen:fontengine=freetype", false},
		{"offscreen with fallback", true, "offscreen;minimal", false},
		{"minimal", true, "minimal", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := []string{"HEADROOM_INSTALL_ROOT=/test root"}
			if test.platform != "" {
				environment = append(environment, "QT_QPA_PLATFORM="+test.platform)
			}
			if got := macOSLaunchUsesAppKit(test.gui, environment); got != test.appKit {
				t.Fatalf("AppKit route = %v, want %v", got, test.appKit)
			}
		})
	}
}
