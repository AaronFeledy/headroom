//go:build !linux

package contract

import (
	"context"
	"errors"
)

// RestartSupervisedManagedService is unsupported outside Linux and WSL.
func RestartSupervisedManagedService(context.Context, string, ManagedServiceRecord, func() error) (ManagedServiceRecord, error) {
	return ManagedServiceRecord{}, errors.New("managed supervised service restart is supported only on Linux and WSL")
}

func managedServiceSupervisor([]string, int) (string, error) { return "", nil }

func validateManagedServiceSupervisor(supervisor string, _ int) error {
	if supervisor != "" {
		return errors.New("managed service supervisor is unsupported")
	}
	return nil
}

func stopManagedServiceSupervisor(string) error {
	return errors.New("managed service supervisor is unsupported")
}
func startManagedServiceSupervisor(string) error {
	return errors.New("managed service supervisor is unsupported")
}
func managedServiceSupervisorPID(string) (int, error) {
	return 0, errors.New("managed service supervisor is unsupported")
}
func supervisedManagedServiceFailure(string) (error, bool) {
	return nil, false
}
