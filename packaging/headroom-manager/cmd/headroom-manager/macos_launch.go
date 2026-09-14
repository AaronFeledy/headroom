package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// Launch Services must open the payload bundle itself. Execing its executable
// from the Finder wrapper leaves macOS associating status items with the
// wrapper, which can make the menu-bar icon disappear on macOS Tahoe.
//
// Pass launch arguments and environment over stdin, never through AppleScript
// interpolation or command-line environment assignments.
const macOSLaunchScript = `ObjC.import('AppKit');
var input = $.NSFileHandle.fileHandleWithStandardInput.readDataToEndOfFile;
var request = JSON.parse(ObjC.unwrap($.NSString.alloc.initWithDataEncoding(input, $.NSUTF8StringEncoding)));
var config = $.NSMutableDictionary.alloc.init;
config.setObjectForKey($(request.arguments), $.NSWorkspaceLaunchConfigurationArguments);
config.setObjectForKey($(request.environment), $.NSWorkspaceLaunchConfigurationEnvironment);
var options = $.NSWorkspaceLaunchNewInstance;
if (request.arguments.indexOf("--background") >= 0) options |= $.NSWorkspaceLaunchWithoutActivation;
var error = Ref();
var app = $.NSWorkspace.sharedWorkspace.launchApplicationAtURLOptionsConfigurationError(
    $.NSURL.fileURLWithPath(request.bundle), options, config, error);
if (!app || app.processIdentifier <= 0) throw new Error('macOS could not launch Headroom');
`

type macOSLaunchRequest struct {
	Bundle      string            `json:"bundle"`
	Arguments   []string          `json:"arguments"`
	Environment map[string]string `json:"environment"`
}

func macOSLaunchPayload(executable string, arguments, environment []string) ([]byte, error) {
	macos := filepath.Dir(executable)
	contents := filepath.Dir(macos)
	bundle := filepath.Dir(contents)
	if !filepath.IsAbs(executable) || filepath.Base(macos) != "MacOS" ||
		filepath.Base(contents) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		return nil, errors.New("Headroom GUI executable is not inside a macOS application bundle")
	}
	request := macOSLaunchRequest{Bundle: bundle, Arguments: append([]string{}, arguments...), Environment: map[string]string{}}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		// These belong to the wrapper process. Launch Services supplies the
		// destination application's own identity.
		if ok && key != "__CFBundleIdentifier" && key != "XPC_SERVICE_NAME" {
			request.Environment[key] = value
		}
	}
	for _, argument := range arguments {
		if argument == "--background" {
			// Qt otherwise activates itself in applicationDidFinishLaunching,
			// overriding Launch Services' without-activation option.
			request.Environment["QT_MAC_DISABLE_FOREGROUND_APPLICATION_TRANSFORM"] = "1"
			break
		}
	}
	return json.Marshal(request)
}
