package usage

import "strings"

// Account presence tells clients whether the user actually has an account with
// a provider. Every usage row is served, so a provider that was never installed
// would otherwise look exactly like one the user signed out of.
const (
	// PresencePresent means the server observed this account or remembers it.
	PresencePresent = "present"
	// PresenceAbsent means nothing observed or remembered an account for the row.
	PresenceAbsent = "absent"
)

// AccountEvidence reports whether one reading or its configuration proves the
// user has an account. Signed-in and expired sign-ins, any discovered source,
// and a source reporting signed_in, expired, or encrypted count. Locked,
// unreadable, and signed_out sources prove nothing: Firefox being open reports
// locked, an unreadable store is not a stored key, and a bad read is not a
// credential. Extra account rows are configured explicitly, so they always
// count.
func (d UsageData) AccountEvidence() bool {
	if d.Account != nil {
		return true
	}
	if d.Auth.State == "signed_in" || d.Auth.State == "expired" {
		return true
	}
	if d.Auth.Source != nil {
		return true
	}
	for _, checked := range d.Auth.Checked {
		switch checked.Status {
		case "signed_in", "expired", "encrypted":
			return true
		}
	}
	return false
}

// RowKey is the stable identity of a usage row: the provider type for a default
// row, or "<provider type>:<account id>" for an extra account row. It matches
// the desktop client's row key.
func (d UsageData) RowKey() string {
	providerType := strings.TrimSpace(d.ProviderType)
	if providerType == "" {
		providerType = strings.TrimSpace(d.ProviderName)
	}
	if d.Account != nil {
		return providerType + ":" + d.Account.ID
	}
	return providerType
}
