package cursor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

var errOptionalBody = errors.New("cursor: invalid optional response")

type optionalEntry struct {
	body     []byte
	observed time.Time
	next     time.Time
}

type optionalCache struct {
	key     [32]byte
	entries map[string]optionalEntry
	mu      sync.Mutex
}

type optionalResult struct {
	period        *currentPeriod
	periodFailure *usage.FetchFailure
	plan          *planInfo
	planObserved  time.Time
	planStale     bool
	grants        *grantObservation
	sand          *cursorSandUsage
}

func decodeOptional(r io.Reader, dst interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r, 256*1024+1))
	if err != nil || len(body) > 256*1024 || len(bytes.TrimSpace(body)) == 0 {
		return errOptionalBody
	}
	if bytes.TrimSpace(body)[0] != '{' {
		return errOptionalBody
	}
	return json.Unmarshal(body, dst)
}

func (c *Client) optionalContext(candidate credential) {
	key := sha256.Sum256([]byte(candidate.cookie + "\x00" + candidate.path + "\x00" + candidate.source.Kind + "\x00" + candidate.source.Name + "\x00" + c.baseURL + "\x00" + c.rpcBaseURL))
	if key != c.optional.key {
		c.optional.key = key
		c.optional.entries = make(map[string]optionalEntry)
		c.weeklyKnown = false
	}
}

func (c *Client) optionalMetadata(ctx context.Context, cookie string) optionalResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result optionalResult
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		result.period, result.periodFailure = c.fetchCurrentPeriod(ctx, cookie)
		e := c.optionalRead(ctx, cookie, planInfoPath)
		var plan planInfoResponse
		if json.Unmarshal(e.body, &plan) == nil {
			result.plan = plan.info()
			result.planObserved = e.observed
			result.planStale = c.discovery.Now().Sub(e.observed) >= 5*time.Minute
		}
	}()
	go func() { defer wg.Done(); result.grants = c.fetchGrants(ctx, cookie) }()
	go func() {
		defer wg.Done()
		sandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		result.sand = c.fetchSandUsage(sandCtx, cookie)
	}()
	wg.Wait()
	return result
}

func (c *Client) optionalRead(ctx context.Context, cookie, path string) optionalEntry {
	now := c.discovery.Now()
	c.optional.mu.Lock()
	entry := c.optional.entries[path]
	c.optional.mu.Unlock()
	if !now.Before(entry.next) {
		child, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var resp *http.Response
		var err error
		if path == planInfoPath || path == sandUsagePath {
			resp, err = c.postJSON(child, path, cookie, []byte("{}"))
		} else {
			resp, err = c.grantRequest(child, cookie, path)
		}
		entry.next = now.Add(5 * time.Minute)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
				var object map[string]json.RawMessage
				if readErr == nil && len(body) <= 256*1024 && json.Unmarshal(body, &object) == nil && object != nil && validOptionalBody(path, body) {
					entry.body, entry.observed = body, now
				}
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				if until, ok := optionalRetryAfter(resp.Header.Get("Retry-After"), now); ok && until.After(entry.next) {
					entry.next = until
				}
			}
		}
		c.optional.mu.Lock()
		c.optional.entries[path] = entry
		c.optional.mu.Unlock()
	}
	if now.Sub(entry.observed) > 15*time.Minute {
		entry.body = nil
	}
	return entry
}

func validOptionalBody(path string, body []byte) bool {
	if path == sandUsagePath {
		var sand cursorSandUsage
		return json.Unmarshal(body, &sand) == nil && (sand.percentReported || sand.HasNonZeroIncludedLimit || sand.HasAvailableUsage != nil || sand.UsesPooledEnterpriseAllowance != nil || safeLabel(sand.GrokPlanLabel) != nil)
	}
	if path != planInfoPath {
		return parseGrants(body) != nil
	}
	var plan planInfoResponse
	if json.Unmarshal(body, &plan) != nil || plan.info() == nil {
		return false
	}
	p := plan.info()
	return safeLabel(p.PlanName) != nil || p.weekly()
}

func optionalRetryAfter(raw string, now time.Time) (time.Time, bool) {
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	date, err := http.ParseTime(raw)
	return date, err == nil
}
