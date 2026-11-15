package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

type listedSignIn struct {
	State      string `json:"state"`
	SourceKind string `json:"source_kind"`
	SourceName string `json:"source_name"`
}

type listedAccount struct {
	ID        string       `json:"id"`
	Label     string       `json:"label"`
	Enabled   bool         `json:"enabled"`
	ConfigDir string       `json:"config_dir"`
	SignIn    listedSignIn `json:"sign_in"`
}

func (o Options) list(ctx context.Context, target accountconfig.Target, asJSON bool) error {
	items, err := accountconfig.List(ctx, target)
	if err != nil {
		return err
	}
	if asJSON {
		result := struct {
			ConfigPath string          `json:"config_path"`
			Accounts   []listedAccount `json:"accounts"`
		}{ConfigPath: target.Path, Accounts: make([]listedAccount, 0, len(items))}
		for _, a := range items {
			probe := o.SignIn(ctx, a.ConfigDir)
			result.Accounts = append(result.Accounts, listedAccount{
				ID: a.ID, Label: a.Label, Enabled: a.Enabled, ConfigDir: a.ConfigDir,
				SignIn: listedSignIn{State: probe.State, SourceKind: probe.SourceKind, SourceName: probe.SourceName},
			})
		}
		return json.NewEncoder(o.Stdout).Encode(result)
	}
	if _, err := fmt.Fprintf(o.Stdout, "Config: %s\n", target.Path); err != nil {
		return err
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(o.Stdout, "No extra Claude accounts. Add one with: headroom accounts add claude <id>")
		return err
	}
	w := tabwriter.NewWriter(o.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tLABEL\tSTATUS\tCONFIG DIR"); err != nil {
		return err
	}
	for _, a := range items {
		state := o.SignIn(ctx, a.ConfigDir).State
		status := signInState(state)
		if state == "locked" {
			status = "keychain locked"
		}
		if !a.Enabled {
			status = "disabled (" + status + ")"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Label, status, a.ConfigDir); err != nil {
			return err
		}
	}
	return w.Flush()
}
