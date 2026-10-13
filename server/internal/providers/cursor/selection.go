package cursor

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/browsercookie"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type fileStamp struct {
	size     int64
	modified time.Time
}

func stamp(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{info.Size(), info.ModTime()}
}

func (c *Client) candidates(ctx context.Context) []credential {
	files := c.discovery.files(c.authPath)
	var result []credential
	for _, candidate := range files {
		if _, err := os.Stat(candidate.path); os.IsNotExist(err) {
			if _, parentErr := os.Stat(filepath.Dir(candidate.path)); parentErr == nil {
				candidate.status = "signed_out"
				result = append(result, candidate)
			}
			continue
		}
		result = append(result, readCredential(ctx, candidate))
	}
	browserFiles := c.discovery.browserFiles()
	changed := c.discovery.Now().Sub(c.browserScanned) >= 5*time.Minute || c.browserScanned.IsZero()
	for _, candidate := range browserFiles {
		if c.stamps[candidate.path] != stamp(candidate.path) {
			changed = true
		}
		if c.stamps[candidate.path+"-wal"] != stamp(candidate.path+"-wal") {
			changed = true
		}
	}
	if changed {
		c.browsers = nil
		for _, candidate := range browserFiles {
			if _, err := os.Stat(candidate.path); os.IsNotExist(err) {
				candidate.status = "signed_out"
				c.browsers = append(c.browsers, candidate)
				continue
			}
			read := browsercookie.Read(ctx, candidate.path, strings.HasPrefix(candidate.source.Name, "Firefox"), c.discovery.Now())
			candidate.cookie, candidate.status = read.Cookie, read.Status
			switch read.Status {
			case "unreadable", "locked", "encrypted":
				candidate.failure = &usage.FetchFailure{Kind: usage.FailureOther}
			}
			c.browsers = append(c.browsers, candidate)
			c.stamps[candidate.path] = stamp(candidate.path)
			c.stamps[candidate.path+"-wal"] = stamp(candidate.path + "-wal")
		}
		c.browserScanned = c.discovery.Now()
	}
	result = append(result, c.browsers...)
	if c.pushed.cookie != "" {
		result = append(result, c.pushed)
	}
	for index := range result {
		if result[index].cookie != "" && (c.isRejected(result[index].cookie) || cookieExpired(result[index].cookie, c.discovery.Now())) {
			c.reject(result[index].cookie)
			result[index].status = "expired"
		}
	}
	return result
}

func cookieExpired(cookie string, now time.Time) bool {
	for _, part := range strings.Split(cookie, ";") {
		_, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			continue
		}
		if _, token, ok := strings.Cut(decoded, "::"); ok {
			decoded = token
		}
		claims, err := decodeJWTClaims(decoded)
		if err == nil && claims.ExpiresAt > 0 && claims.ExpiresAt <= now.Unix() {
			return true
		}
	}
	return false
}

func (c *Client) isRejected(cookie string) bool {
	hash := sha256.Sum256([]byte(cookie))
	for _, rejected := range c.rejected {
		if rejected == hash {
			return true
		}
	}
	return false
}

func (c *Client) reject(cookie string) {
	if c.isRejected(cookie) {
		return
	}
	if len(c.rejected) == 32 {
		c.rejected = c.rejected[1:]
	}
	c.rejected = append(c.rejected, sha256.Sum256([]byte(cookie)))
}

func (c *Client) adopt(candidate credential) {
	if c.active.source != candidate.source {
		c.logger.Info("cursor credential source changed", slog.String("kind", candidate.source.Kind), slog.String("name", candidate.source.Name))
	}
	c.active = candidate
	c.secret.set(candidate.cookie)
}

func (c *Client) attachAuth(data usage.UsageData, candidates []credential, selected *credential) usage.UsageData {
	state := "signed_out"
	var source *usage.AuthSource
	if selected != nil {
		state = "signed_in"
		value := selected.source
		source = &value
	} else {
		if c.active.cookie != "" {
			value := c.active.source
			source = &value
		}
		for _, candidate := range candidates {
			if candidate.status == "expired" {
				state = "expired"
				if source == nil {
					value := candidate.source
					source = &value
				}
				break
			}
		}
		if state == "signed_out" {
			source = nil
		}
	}
	data.Auth = usage.NewAuth(providerName, state, source)
	for _, candidate := range candidates {
		data.Auth.Checked = append(data.Auth.Checked, usage.AuthChecked{Kind: candidate.source.Kind, Name: candidate.source.Name, Status: candidate.status})
	}
	if len(data.Auth.Checked) > 12 {
		data.Auth.Checked = data.Auth.Checked[:12]
	}
	data.NeedsReauth = state != "signed_in"
	data.ReauthCommand = data.Auth.SignInCommand
	if state != "signed_in" {
		message := cursorAuthError(data.Auth)
		data.Error = &message
	}
	return data
}

func cursorAuthError(auth usage.Auth) string {
	if auth.State == "signed_out" {
		return "Not signed in to Cursor. Run `cursor-agent login` or sign in to cursor.com in a browser."
	}
	source := auth.Source
	if source == nil {
		return "The Cursor credential sent to the server expired. Send a new one."
	}
	switch source.Kind {
	case "cli":
		return "Your cursor-agent login expired. Run `cursor-agent login` again."
	case "app":
		return "Your Cursor app sign-in expired. Open Cursor and sign in again."
	case "browser":
		return "Your " + source.Name + " sign-in to cursor.com expired. Sign in to cursor.com in " + source.Name + " again."
	case "desktop":
		if source.Name == "browser" {
			return "The browser sign-in shared by Headroom expired. Sign in to cursor.com again."
		}
		return "The " + source.Name + " sign-in shared by Headroom expired. Sign in to cursor.com in " + source.Name + " again."
	default:
		return "The Cursor credential sent to the server expired. Send a new one."
	}
}
