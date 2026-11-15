//go:build !windows

package claude

// wslHomeDiscoveryEnabled mirrors the Windows switch so tests can share one
// TestMain across platforms; only Windows discovery consults it.
var wslHomeDiscoveryEnabled = true

func defaultWSLHome() string { return "" }
