package opencodego

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func Test_Provider_Fetch_tracks_V1_upgrade_account_change_and_V2_sign_out(t *testing.T) {
	// Given one provider initially signed in through V1
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"A"}}`)
	setModTime(t, authPath, millisAt(-4*time.Hour))
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())
	database := filepath.Join(localDataDirectory(home), "opencode.db")

	// When V2 imports V1, changes accounts, and then deletes its last credential
	first := fetch(t, provider)
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-3*time.Hour))
	insertCredential(t, database, credentialFixture{id: "imported", integration: "opencode-go", value: `{"type":"key","key":"A"}`, createdMS: millisAt(-3 * time.Hour), updatedMS: millisAt(-3 * time.Hour)})
	second := fetch(t, provider)
	updateCredential(t, database, `DELETE FROM credential WHERE id = ?`, "imported")
	insertCredential(t, database, credentialFixture{id: "new-account", integration: "opencode-go", value: `{"type":"key","key":"B"}`, active: activeFlag(1), createdMS: millisAt(-time.Hour), updatedMS: millisAt(-time.Hour)})
	third := fetch(t, provider)
	updateCredential(t, database, `DELETE FROM credential WHERE id = ?`, "new-account")
	fourth := fetch(t, provider)

	// Then the imported key never revives and logout makes no upstream request
	states := []string{first.Auth.State, second.Auth.State, third.Auth.State, fourth.Auth.State}
	if !slices.Equal(states, []string{"signed_in", "signed_in", "signed_in", "signed_out"}) {
		t.Fatalf("states = %v", states)
	}
	auths, _, requests := server.recorded()
	if requests != 3 || !slices.Equal(auths, []string{"Bearer A", "Bearer A", "Bearer B"}) {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}

func Test_Provider_Fetch_tracks_dual_install_V1_sign_in_and_sign_out(t *testing.T) {
	// Given a live V2 account and a provider that survives both V1 transitions
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-3*time.Hour))
	insertCredential(t, database, credentialFixture{id: "v2", integration: "opencode-go", value: `{"type":"key","key":"B"}`, active: activeFlag(1), createdMS: millisAt(-2 * time.Hour), updatedMS: millisAt(-2 * time.Hour)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When V1 signs in later, then removes only its own OpenCode Go entry
	first := fetch(t, provider)
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"C"}}`)
	setModTime(t, authPath, millisAt(-time.Hour))
	second := fetch(t, provider)
	authPath = writeLegacyAuth(t, home, `{"cursor":{"type":"api","key":"other"}}`)
	setModTime(t, authPath, millisAt(0))
	third := fetch(t, provider)

	// Then V1's logout leaves the still-live V2 account available
	states := []string{first.Auth.State, second.Auth.State, third.Auth.State}
	if !slices.Equal(states, []string{"signed_in", "signed_in", "signed_in"}) {
		t.Fatalf("states = %v", states)
	}
	auths, _, requests := server.recorded()
	if requests != 3 || !slices.Equal(auths, []string{"Bearer B", "Bearer C", "Bearer B"}) {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}
