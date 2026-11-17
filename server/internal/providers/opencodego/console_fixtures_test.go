package opencodego

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// consoleAccountSchema and consoleAccountStateSchema are the CLI's console
// sign-in tables, byte for byte.
const consoleAccountSchema = "CREATE TABLE `account` (\n" +
	"          `id` text PRIMARY KEY,\n" +
	"          `email` text NOT NULL,\n" +
	"          `url` text NOT NULL,\n" +
	"          `access_token` text NOT NULL,\n" +
	"          `refresh_token` text NOT NULL,\n" +
	"          `token_expiry` integer,\n" +
	"          `time_created` integer NOT NULL,\n" +
	"          `time_updated` integer NOT NULL\n" +
	"        )"

const consoleAccountStateSchema = "CREATE TABLE `account_state` (\n" +
	"          `id` integer PRIMARY KEY,\n" +
	"          `active_account_id` text,\n" +
	"          `active_org_id` text,\n" +
	"          CONSTRAINT `fk_account_state_active_account_id_account_id_fk` FOREIGN KEY (`active_account_id`) REFERENCES `account`(`id`) ON DELETE SET NULL\n" +
	"        )"

// consoleFixture is one row in the CLI's console account table.
type consoleFixture struct {
	id           string
	email        string
	url          string
	accessToken  string
	refreshToken string
	expiryMS     *int64
	createdMS    int64
	updatedMS    int64
}

func createConsoleTables(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{consoleAccountSchema, consoleAccountStateSchema} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func insertConsoleSignIn(t *testing.T, path string, account consoleFixture, orgID *string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var expiry any
	if account.expiryMS != nil {
		expiry = *account.expiryMS
	}
	var org any
	if orgID != nil {
		org = *orgID
	}
	if _, err := database.Exec(
		`INSERT INTO account (id, email, url, access_token, refresh_token, token_expiry, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		account.id, account.email, account.url, account.accessToken, account.refreshToken, expiry, account.createdMS, account.updatedMS,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO account_state (id, active_account_id, active_org_id) VALUES (1, ?, ?)`, account.id, org); err != nil {
		t.Fatal(err)
	}
}

func int64Ptr(value int64) *int64 { return &value }
