//go:build !windows && !darwin

package main

import "syscall"

func showLaunchError(string) {}

func startApplication(executable string, arguments, environment []string, _ bool) error {
	return syscall.Exec(executable, append([]string{executable}, arguments...), environment)
}
