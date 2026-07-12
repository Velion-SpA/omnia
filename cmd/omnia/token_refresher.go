package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/velion/omnia/internal/cloud/remote"
	"github.com/velion/omnia/internal/cloud/tokenrefresh"
	"github.com/velion/omnia/internal/envx"
	"github.com/velion/omnia/internal/store"
)

// tokenRefreshInterval is how often the background refresher wakes up to check
// whether the stored token needs renewal. A 1h interval is well within the 24h
// account-token TTL (6h safety floor) while being cheap on resources.
const tokenRefreshInterval = 1 * time.Hour

// tokenRefreshHTTPTimeout is the per-request timeout for POST /auth/refresh.
const tokenRefreshHTTPTimeout = 30 * time.Second

// tokenRefreshInitialDelay is how long the refresher waits after startup before
// its first check. A short delay lets server initialization settle while still
// catching a token that is already near expiry at boot — before the first full
// interval elapses.
const tokenRefreshInitialDelay = 30 * time.Second

// startTokenRefresher starts a background goroutine that proactively rotates
// the account token for a single cloud alias. It:
//
//  1. Reads the current token from cloud.json (or the env override for the
//     default alias) each tick to pick up external changes.
//  2. Skips if the env override is active for the default alias — that token
//     is not from cloud.json and must not be overwritten.
//  3. Skips non-account tokens (managed / legacy bearer / dashboard sessions).
//  4. Skips tokens that are already expired (server would reject the refresh).
//  5. Calls POST /auth/refresh, persists the new token to cloud.json, and
//     calls SetToken on every provided MutationTransport so in-flight requests
//     immediately benefit from the fresh credential.
//
// The goroutine stops when ctx is cancelled. It is started alongside the
// autosync.Manager(s) for the same alias and shares the same context.
func startTokenRefresher(
	ctx context.Context,
	cfg store.Config,
	alias string,
	applyEnvOverrides bool,
	transports []*remote.MutationTransport,
) {
	go runTokenRefresher(ctx, cfg, alias, applyEnvOverrides, transports, tokenRefreshInterval, tokenRefreshInitialDelay, time.Now)
}

// runTokenRefresher is the testable inner loop. now is injectable for tests.
func runTokenRefresher(
	ctx context.Context,
	cfg store.Config,
	alias string,
	applyEnvOverrides bool,
	transports []*remote.MutationTransport,
	interval time.Duration,
	initialDelay time.Duration,
	nowFn func() time.Time,
) {
	label := cloudAliasLabel(alias)
	httpClient := &http.Client{Timeout: tokenRefreshHTTPTimeout}

	// Log once if env override is active (footgun warning). Check before arming
	// any timers so we exit cleanly without scheduling work.
	envOverrideActive := applyEnvOverrides && strings.TrimSpace(envx.Get("OMNIA_CLOUD_TOKEN")) != ""
	if envOverrideActive {
		log.Printf("[tokenrefresh] cloud %q: OMNIA_CLOUD_TOKEN env override is active; proactive token refresh disabled for this cloud (token is not from cloud.json)", label)
		return
	}

	// Fire one check shortly after startup (grace period for server init) so a
	// token already near expiry at boot is refreshed without waiting a full
	// interval, then tick periodically thereafter.
	initialTimer := time.NewTimer(initialDelay)
	defer initialTimer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-initialTimer.C:
			tryRefreshToken(ctx, cfg, alias, label, httpClient, transports, nowFn)
		case <-ticker.C:
			tryRefreshToken(ctx, cfg, alias, label, httpClient, transports, nowFn)
		}
	}
}

// tryRefreshToken performs one refresh attempt. It is idempotent and
// non-fatal: any error is logged and the next tick will retry.
func tryRefreshToken(
	ctx context.Context,
	cfg store.Config,
	alias string,
	label string,
	httpClient *http.Client,
	transports []*remote.MutationTransport,
	nowFn func() time.Time,
) {
	// Re-read cloud.json each tick so external token rotations (e.g. manual
	// `omnia cloud refresh`) are picked up automatically.
	v2, err := loadCloudConfigV2(cfg)
	if err != nil {
		log.Printf("[tokenrefresh] cloud %q: could not read cloud config: %v", label, err)
		return
	}

	// Resolve the stored token for this alias.
	var currentToken, serverURL string
	if v2 != nil {
		var entry *cloudEntry
		if strings.TrimSpace(alias) != "" {
			e, ok := v2.getCloud(alias)
			if ok {
				entry = e
			}
		} else {
			entry = v2.defaultCloudEntry()
		}
		if entry != nil {
			currentToken = strings.TrimSpace(entry.Token)
			serverURL = strings.TrimSpace(entry.ServerURL)
		}
	}

	if currentToken == "" || serverURL == "" {
		// No credentials configured — nothing to refresh.
		return
	}

	// Decode the claims portion without verifying HMAC.
	info, err := tokenrefresh.ParseAccountTokenClaims(currentToken)
	if err != nil {
		log.Printf("[tokenrefresh] cloud %q: token parse error: %v", label, err)
		return
	}
	if info == nil {
		// Non-account token (managed, legacy bearer, or dashboard session) — skip.
		return
	}

	if !tokenrefresh.ShouldRefresh(info, nowFn()) {
		return
	}

	// Check again if the token is already expired before calling the server
	// (belt-and-suspenders guard; ShouldRefresh already handles this, but log a
	// distinct message for operator visibility).
	if info.ExpiresAt <= nowFn().Unix() {
		log.Printf("[tokenrefresh] cloud %q: stored token is already expired; skipping proactive refresh (run `omnia cloud login` to re-authenticate)", label)
		return
	}

	log.Printf("[tokenrefresh] cloud %q: token nearing expiry — refreshing proactively", label)

	newToken, err := tokenrefresh.RefreshAccountToken(ctx, httpClient, serverURL, currentToken)
	if err != nil {
		log.Printf("[tokenrefresh] cloud %q: refresh failed: %v", label, err)
		return
	}

	// Persist the new token to cloud.json.
	if err := saveCloudConfigV2Entry(cfg, alias, "", newToken, ""); err != nil {
		log.Printf("[tokenrefresh] cloud %q: failed to persist refreshed token to cloud.json: %v", label, err)
		return
	}

	// Update in-memory token on all running transports so the very next
	// request uses the fresh credential without requiring a restart.
	for _, t := range transports {
		t.SetToken(newToken)
	}

	log.Printf("[tokenrefresh] cloud %q: token refreshed and persisted successfully", label)
}
