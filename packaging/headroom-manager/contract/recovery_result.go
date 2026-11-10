package contract

import (
	"encoding/json"
	"path/filepath"
)

func ensureAppliedResult(root string, candidate InstallState) error {
	path := filepath.Join(root, "last-apply-result.json")
	data, err := readBoundedFile(path, maxManifestBytes)
	var result ApplyResult
	if err == nil && json.Unmarshal(data, &result) == nil && result.Schema == SchemaVersion && result.Product == "Headroom" &&
		result.Status == "applied" && result.Version == candidate.ActiveVersion && result.VersionPath == candidate.VersionPath && !result.RolledBack {
		return nil
	}
	return writeDurableJSON(path, ApplyResult{Schema: SchemaVersion, Product: "Headroom", Status: "applied",
		Version: candidate.ActiveVersion, VersionPath: candidate.VersionPath})
}
