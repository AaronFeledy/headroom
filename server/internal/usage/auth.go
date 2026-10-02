package usage

import "encoding/json"

type AuthSource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type AuthChecked struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Auth struct {
	State                     string        `json:"state"`
	Source                    *AuthSource   `json:"source"`
	SignInCommand             *string       `json:"sign_in_command"`
	SignInURL                 *string       `json:"sign_in_url"`
	AcceptsBrowserCredentials bool          `json:"accepts_browser_credentials"`
	Checked                   []AuthChecked `json:"checked"`
}

func NewAuth(provider, state string, source *AuthSource) Auth {
	a := Auth{State: state, Source: source, Checked: []AuthChecked{}, AcceptsBrowserCredentials: provider == "Cursor"}
	if state == "signed_in" {
		return a
	}
	commands := map[string]string{"Cursor": "cursor-agent login", "Claude": "claude auth login", "Codex": "codex login", "Grok": "grok login"}
	if state == "signed_out" || (source != nil && source.Kind == "cli") {
		if command, ok := commands[provider]; ok {
			a.SignInCommand = &command
		}
	}
	if provider == "Cursor" && (source == nil || source.Kind != "cli") {
		url := "https://cursor.com/login"
		a.SignInURL = &url
	}
	return a
}

func (a Auth) MarshalJSON() ([]byte, error) {
	type wire Auth
	if a.Checked == nil {
		a.Checked = []AuthChecked{}
	}
	if len(a.Checked) > 12 {
		a.Checked = a.Checked[:12]
	}
	return json.Marshal(wire(a))
}
