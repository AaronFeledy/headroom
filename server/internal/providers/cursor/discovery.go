package cursor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/browsercookie"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

type Discovery struct {
	Environment        winprofile.Environment
	Now                func() time.Time
	BrowserCredentials bool
}

type credential struct {
	failure *usage.FetchFailure
	source  usage.AuthSource
	path    string
	cookie  string
	status  string
}

func defaultDiscovery() Discovery {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return Discovery{Environment: winprofile.Environment{GOOS: runtime.GOOS, Home: home, Env: os.Getenv}, Now: time.Now, BrowserCredentials: true}
}

func (d Discovery) files(authPath string) []credential {
	e := d.Environment
	root := e.Env("XDG_CONFIG_HOME")
	if root == "" {
		root = filepath.Join(e.Home, ".config")
	}
	cli := filepath.Join(root, "cursor", "auth.json")
	appRoot := filepath.Join(root, "Cursor")
	switch e.GOOS {
	case "darwin":
		cli = filepath.Join(e.Home, ".cursor", "auth.json")
		appRoot = filepath.Join(e.Home, "Library", "Application Support", "Cursor")
	case "windows":
		roaming := e.Env("APPDATA")
		if roaming == "" {
			roaming = filepath.Join(e.Home, "AppData", "Roaming")
		}
		appRoot = filepath.Join(roaming, "Cursor")
		cli = filepath.Join(appRoot, "auth.json")
	}
	if authPath != "" {
		cli = authPath
	}
	result := []credential{{source: usage.AuthSource{Kind: "cli", Name: "cursor-agent"}, path: cli}}
	windows := e.WindowsProfiles()
	if authPath == "" {
		for _, home := range e.WSLHomes() {
			result = append(result, credential{source: usage.AuthSource{Kind: "cli", Name: "cursor-agent (WSL)"}, path: filepath.Join(home, ".config", "cursor", "auth.json")})
		}
		for _, home := range windows {
			result = append(result, credential{source: usage.AuthSource{Kind: "cli", Name: "cursor-agent (Windows)"}, path: filepath.Join(home, "AppData", "Roaming", "Cursor", "auth.json")})
		}
	}
	result = append(result, credential{source: usage.AuthSource{Kind: "app", Name: "Cursor"}, path: filepath.Join(appRoot, "User", "globalStorage", "state.vscdb")})
	for _, home := range windows {
		result = append(result, credential{source: usage.AuthSource{Kind: "app", Name: "Cursor (Windows)"}, path: filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "globalStorage", "state.vscdb")})
	}
	return result
}

func readCredential(ctx context.Context, candidate credential) credential {
	candidate.status = "signed_out"
	var err error
	if candidate.source.Kind == "app" {
		var token string
		token, err = browsercookie.AppToken(ctx, candidate.path)
		if err == nil && token != "" {
			candidate.cookie, err = cookieFromAccessToken(strings.Trim(token, "\""))
		}
	} else {
		candidate.cookie, err = readLocalCookieHeader(ctx, candidate.path)
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrUnauthorized) {
		return candidate
	}
	if err != nil {
		candidate.status = "unreadable"
		candidate.failure = &usage.FetchFailure{Kind: usage.FailureOther}
		return candidate
	}
	if candidate.cookie != "" {
		candidate.status = "signed_in"
	}
	return candidate
}

func (d Discovery) browserFiles() []credential {
	if !d.BrowserCredentials {
		return nil
	}
	e := d.Environment
	config := e.Env("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(e.Home, ".config")
	}
	var firefox []string
	var chromeRoots []string
	switch e.GOOS {
	case "windows":
		roaming, local := e.Env("APPDATA"), e.Env("LOCALAPPDATA")
		if roaming == "" {
			roaming = filepath.Join(e.Home, "AppData", "Roaming")
		}
		if local == "" {
			local = filepath.Join(e.Home, "AppData", "Local")
		}
		firefox = []string{filepath.Join(roaming, "Mozilla", "Firefox")}
		chromeRoots = windowsChromeRoots(local)
	case "darwin":
		base := filepath.Join(e.Home, "Library", "Application Support")
		firefox = []string{filepath.Join(base, "Firefox")}
		chromeRoots = []string{filepath.Join(base, "Google", "Chrome"), filepath.Join(base, "Microsoft Edge"), filepath.Join(base, "BraveSoftware", "Brave-Browser"), filepath.Join(base, "Chromium")}
	default:
		firefox = []string{filepath.Join(e.Home, ".mozilla", "firefox"), filepath.Join(config, "mozilla", "firefox"), filepath.Join(e.Home, "snap", "firefox", "common", ".mozilla", "firefox"), filepath.Join(e.Home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox")}
		chromeRoots = []string{filepath.Join(config, "google-chrome"), filepath.Join(config, "microsoft-edge"), filepath.Join(config, "BraveSoftware", "Brave-Browser"), filepath.Join(config, "chromium")}
	}
	result := browserCandidates(firefox, chromeRoots, "")
	for _, home := range e.WindowsProfiles() {
		result = append(result, browserCandidates([]string{filepath.Join(home, "AppData", "Roaming", "Mozilla", "Firefox")}, windowsChromeRoots(filepath.Join(home, "AppData", "Local")), " (Windows)")...)
	}
	return result
}

func windowsChromeRoots(local string) []string {
	return []string{filepath.Join(local, "Google", "Chrome", "User Data"), filepath.Join(local, "Microsoft", "Edge", "User Data"), filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data"), filepath.Join(local, "Chromium", "User Data")}
}

func browserCandidates(firefox, chrome []string, suffix string) []credential {
	var result []credential
	for _, root := range firefox {
		for _, profile := range browsercookie.FirefoxProfiles(root) {
			result = append(result, credential{source: usage.AuthSource{Kind: "browser", Name: "Firefox" + suffix}, path: filepath.Join(profile, "cookies.sqlite")})
		}
	}
	for index, root := range chrome {
		for _, profile := range browsercookie.ChromiumProfiles(root) {
			path := filepath.Join(profile, "Network", "Cookies")
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				path = filepath.Join(profile, "Cookies")
			}
			result = append(result, credential{source: usage.AuthSource{Kind: "browser", Name: []string{"Chrome", "Edge", "Brave", "Chromium"}[index] + suffix}, path: path})
		}
	}
	return result
}
