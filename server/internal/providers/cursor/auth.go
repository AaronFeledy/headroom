package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type localAuthFile struct {
	AccessToken string `json:"accessToken"`
}

type jwtClaims struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
}

func cookieFromAccessToken(accessToken string) (string, error) {
	trimmed := strings.TrimSpace(accessToken)
	claims, err := decodeJWTClaims(trimmed)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return "", fmt.Errorf("missing subject: %w", ErrInvalidToken)
	}
	subjectParts := strings.Split(claims.Subject, "|")
	userID := subjectParts[len(subjectParts)-1]
	if userID == "" {
		return "", fmt.Errorf("missing user ID: %w", ErrInvalidToken)
	}
	for _, char := range userID {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return "", fmt.Errorf("invalid user ID: %w", ErrInvalidToken)
		}
	}
	return "WorkosCursorSessionToken=" + userID + "%3A%3A" + trimmed, nil
}

func decodeJWTClaims(accessToken string) (jwtClaims, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return jwtClaims{}, fmt.Errorf("jwt parts: %w", ErrInvalidToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode jwt payload: %w", ErrInvalidToken)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, fmt.Errorf("parse jwt payload: %w", ErrInvalidToken)
	}
	return claims, nil
}

func readLocalCookieHeader(ctx context.Context, authPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := authPath
	if strings.TrimSpace(path) == "" {
		path = defaultAuthPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Cursor auth file: %w", err)
	}
	var auth localAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("parse Cursor auth file: %w", err)
	}
	if strings.TrimSpace(auth.AccessToken) == "" {
		return "", ErrUnauthorized
	}
	cookieHeader, err := cookieFromAccessToken(auth.AccessToken)
	if err != nil {
		return "", err
	}
	return cookieHeader, nil
}

func defaultAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "auth.json"
	}
	switch runtime.GOOS {
	case "windows":
		root := os.Getenv("APPDATA")
		if root == "" {
			root = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(root, "Cursor", "auth.json")
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	default:
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		return filepath.Join(root, "cursor", "auth.json")
	}
}
