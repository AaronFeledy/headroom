package contract

import "testing"

func Test_ServiceEnvironment_preserves_TLS_and_browser_configuration(t *testing.T) {
	// Given
	env := []string{"USAGE_TLS=on", "USAGE_TLS_CERT_FILE=fixture-cert", "USAGE_TLS_KEY_FILE=fixture-key", "USAGE_PROVIDER_CURSOR_BROWSER_CREDENTIALS=false"}
	// When
	filtered := managedServiceEnvironment(env)
	err := validateServiceEnvironment(env)
	// Then
	if err != nil || len(filtered) != len(env) {
		t.Fatalf("filtered=%v err=%v", filtered, err)
	}
}
