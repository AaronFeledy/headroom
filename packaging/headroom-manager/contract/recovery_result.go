package contract

import (
	"encoding/json"
	"path/filepath"
)

type ApplyResult struct {
	Nonce         string `json:"nonce"`
	Schema        int    `json:"schema"`
	Product       string `json:"product"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	VersionPath   string `json:"version_path"`
	RolledBack    bool   `json:"rolled_back"`
	FailureReason string `json:"failure_reason,omitempty"`
}

func ensureAppliedResult(root string, candidate InstallState, nonce string) error {
	path := filepath.Join(root, "last-apply-result.json")
	data, err := readBoundedFile(path, maxManifestBytes)
	var result ApplyResult
	if err == nil && json.Unmarshal(data, &result) == nil && result.Schema == SchemaVersion && result.Product == "Headroom" &&
		result.Nonce == nonce && result.Status == "applied" && result.Version == candidate.ActiveVersion && result.VersionPath == candidate.VersionPath && !result.RolledBack {
		return nil
	}
	return writeDurableJSON(path, ApplyResult{Nonce: nonce, Schema: SchemaVersion, Product: "Headroom", Status: "applied",
		Version: candidate.ActiveVersion, VersionPath: candidate.VersionPath})
}
