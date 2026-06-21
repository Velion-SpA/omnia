package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/velion/omnia/internal/store"
)

// cmdCloudRefresh implements `engram cloud refresh`.
// It reads the stored token for the alias, exchanges it for a fresh token via
// POST /auth/refresh, and overwrites the stored token on success.
func cmdCloudRefresh(cfg store.Config) {
	fs := flag.NewFlagSet("engram cloud refresh", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	cloudAlias := fs.String("cloud", "", "cloud alias to refresh against (default: default cloud)")

	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	v2, _ := loadCloudConfigV2(cfg) // best-effort; nil is fine
	alias := strings.TrimSpace(*cloudAlias)

	serverURL, err := resolveCloudServerForAlias(*server, alias, v2)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	// Resolve the current stored token for this alias.
	var currentToken string
	if v2 != nil {
		var entry *cloudEntry
		if alias != "" {
			e, ok := v2.getCloud(alias)
			if ok {
				entry = e
			}
		} else {
			entry = v2.defaultCloudEntry()
		}
		if entry != nil {
			currentToken = strings.TrimSpace(entry.Token)
		}
	}
	if currentToken == "" {
		fmt.Fprintln(os.Stderr, "error: no token stored for this cloud alias; run `engram cloud login` first")
		exitFunc(1)
		return
	}

	// POST to /auth/refresh with the current token in the Authorization header.
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, serverURL+"/auth/refresh", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh request failed: %v\n", err)
		exitFunc(1)
		return
	}
	req.Header.Set("Authorization", "Bearer "+currentToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh request failed: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "refresh failed (%d): %s\n", resp.StatusCode, serverError(body))
		exitFunc(1)
		return
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		fmt.Fprintf(os.Stderr, "refresh succeeded but token missing in response: %v\n", err)
		exitFunc(1)
		return
	}

	// Overwrite the stored token with the refreshed one.
	if err := saveCloudConfigV2Entry(cfg, alias, "", result.Token, ""); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save refreshed token to cloud.json: %v\n", err)
		exitFunc(1)
		return
	}
	fmt.Println("Token refreshed and stored in cloud.json")
}
