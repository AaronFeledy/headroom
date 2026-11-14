package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
)

const maximumResponseBytes = 1 << 20

func fetchUsage(ctx context.Context, options Options, flags dashboardFlags) (LocalSnapshot, []Provider, error) {
	var body []byte
	var err error
	var snapshot LocalSnapshot
	if flags.ssh != "" {
		body, err = fetchSSH(ctx, options, flags.ssh)
	} else if flags.url == "" && options.FetchLocal != nil {
		snapshot, err = options.FetchLocal(ctx)
		body = snapshot.Body
		if errors.Is(err, ErrLocalUnavailable) {
			body, err = fetchHTTP(ctx, options, "")
			snapshot = LocalSnapshot{}
		}
	} else {
		body, err = fetchHTTP(ctx, options, flags.url)
	}
	if err != nil {
		return LocalSnapshot{}, nil, err
	}
	if len(body) == 0 || len(body) > maximumResponseBytes {
		return LocalSnapshot{}, nil, errors.New("usage response exceeded the size limit")
	}
	providers, err := decodeUsage(body)
	if err != nil {
		return LocalSnapshot{}, nil, err
	}
	if flags.weeklyOnly {
		for index := range providers {
			filtered := providers[index].Buckets[:0]
			for _, bucket := range providers[index].Buckets {
				if weeklyBucket(providers[index].Type(), bucket) {
					filtered = append(filtered, bucket)
				}
			}
			providers[index].Buckets = filtered
		}
		body, err = filterWeeklyJSON(body)
		if err != nil {
			return LocalSnapshot{}, nil, err
		}
	}
	if len(snapshot.Body) == 0 && !flags.json {
		providers = groupProvidersByType(providers)
	}
	snapshot.Body = bytes.TrimSpace(body)
	return snapshot, providers, nil
}

func fetchHTTP(ctx context.Context, options Options, base string) ([]byte, error) {
	usingDefault := base == ""
	if base == "" {
		base = "http://127.0.0.1:7823"
	}
	endpoint, err := usageURL(base)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Headroom/"+options.Version)
	// Automatic localhost discovery never sends a saved token. An address the
	// user selected explicitly may be a token-protected loopback server.
	token := ""
	if !usingDefault {
		token, err = readToken(options.Env)
		if err != nil {
			return nil, err
		}
	}
	var response *http.Response
	if token != "" {
		response, err = authenticatedRequest(ctx, options, request, token)
	} else {
		response, err = options.HTTPClient.Do(request)
	}
	if err != nil {
		if usingDefault {
			return nil, fmt.Errorf("local Headroom usage is unavailable; start 'headroom serve' or use --url/--ssh: %w", err)
		}
		return nil, fmt.Errorf("fetch usage: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(body) > maximumResponseBytes {
		return nil, errors.New("usage response exceeded the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage server returned HTTP %d", response.StatusCode)
	}
	return body, nil
}

func usageURL(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(base, "#") {
		return "", errors.New("--url must be an HTTP(S) base URL without credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v1/usage"
	return parsed.String(), nil
}

func readToken(env []string) (string, error) {
	direct, file := envValue(env, "HEADROOM_AUTH_TOKEN"), envValue(env, "HEADROOM_AUTH_TOKEN_FILE")
	if direct != "" && file != "" {
		return "", errors.New("set only one of HEADROOM_AUTH_TOKEN and HEADROOM_AUTH_TOKEN_FILE")
	}
	if direct != "" {
		if len(direct) > 64*1024 || strings.ContainsAny(direct, "\r\n\x00") {
			return "", errors.New("HEADROOM_AUTH_TOKEN is invalid or too long")
		}
		return direct, nil
	}
	if file == "" {
		return "", nil
	}
	if runtime.GOOS == "windows" {
		return "", errors.New("use HEADROOM_AUTH_TOKEN on Windows; token files require verified owner-only permissions")
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", errors.New("token file is missing or unsafe")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return "", errors.New("token file must be readable only by its owner")
	}
	handle, err := os.Open(file)
	if err != nil {
		return "", errors.New("could not read token file")
	}
	defer handle.Close()
	openedInfo, err := handle.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() > 64*1024 {
		return "", errors.New("token file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(handle, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return "", errors.New("could not read token file")
	}
	token := strings.TrimSuffix(string(data), "\n")
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("token file must contain one nonempty line")
	}
	return token, nil
}
