package main

import (
	"bytes"
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

// cloudDeviceInfo is the client-side view of a device returned by GET /devices.
type cloudDeviceInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ScopeProjects []string `json:"scope_projects"`
	LastSeenAt    string   `json:"last_seen_at"`
}

// cmdCloudDevices implements `omnia cloud devices <list|scope|revoke>`.
//
// It manages the per-account device registry that backs per-device project scope
// (the two-notebook homelab isolation test). All subcommands authenticate with the
// account token stored in cloud.json and address devices by friendly NAME, mapping
// name → id via the GET /devices endpoint.
func cmdCloudDevices(cfg store.Config) {
	if len(os.Args) < 4 {
		printCloudDevicesUsage(os.Stderr)
		exitFunc(1)
		return
	}
	switch os.Args[3] {
	case "--help", "-h", "help":
		printCloudDevicesUsage(os.Stdout)
		return
	case "list":
		cmdCloudDevicesList(cfg)
	case "scope":
		cmdCloudDevicesScope(cfg)
	case "revoke":
		cmdCloudDevicesRevoke(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown cloud devices command: %s\n", os.Args[3])
		printCloudDevicesUsage(os.Stderr)
		exitFunc(1)
	}
}

func printCloudDevicesUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: omnia cloud devices <list|scope|revoke> [options]")
	fmt.Fprintln(w, "  list                                 list this account's devices, scope, and last-seen")
	fmt.Fprintln(w, "  scope <device> --projects a,b,c      restrict a device to the given projects (empty = unrestricted)")
	fmt.Fprintln(w, "  revoke <device>                      delete a device (denies its scope immediately, fail-closed)")
	fmt.Fprintln(w, "options: --cloud <alias>  --server <url>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "note: revoking a device denies its project scope IMMEDIATELY (fail-closed), but an")
	fmt.Fprintln(w, "      account token already issued for that device stays valid until it expires or is")
	fmt.Fprintln(w, "      refreshed. Cryptographic token-device binding is separate, optional hardening.")
}

// doAuthedCloudRequest issues an authenticated request with the account bearer
// token. payload is JSON-encoded when non-nil. The caller closes the body.
func doAuthedCloudRequest(method, url, token string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// resolveDeviceTarget returns the server URL and account token for device-management
// requests. The token always comes from the stored cloud.json entry (device
// management requires a logged-in account); --server only overrides the request URL.
func resolveDeviceTarget(cfg store.Config, flagServer, alias string) (serverURL, token string, err error) {
	v2, _ := loadCloudConfigV2(cfg) // best-effort; nil is fine
	if alias != "" && v2 != nil {
		if _, ok := v2.getCloud(alias); !ok {
			return "", "", fmt.Errorf("cloud %q not found; run `omnia cloud add %s --server <url>` first", alias, alias)
		}
	}
	serverURL, err = resolveCloudServerForAlias(flagServer, alias, v2)
	if err != nil {
		return "", "", err
	}
	var entry *cloudEntry
	if v2 != nil {
		if alias != "" {
			entry, _ = v2.getCloud(alias)
		} else {
			entry = v2.defaultCloudEntry()
		}
	}
	if entry == nil || strings.TrimSpace(entry.Token) == "" {
		hint := ""
		if alias != "" {
			hint = " --cloud " + alias
		}
		return "", "", fmt.Errorf("not logged in to this cloud; run `omnia cloud login --username <u>%s` first", hint)
	}
	return serverURL, strings.TrimSpace(entry.Token), nil
}

// fetchDevices retrieves the account's devices from GET /devices.
func fetchDevices(serverURL, token string) ([]cloudDeviceInfo, error) {
	resp, err := doAuthedCloudRequest(http.MethodGet, serverURL+"/devices", token, nil)
	if err != nil {
		return nil, fmt.Errorf("list devices request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var devices []cloudDeviceInfo
		if err := json.Unmarshal(body, &devices); err != nil {
			return nil, fmt.Errorf("could not parse devices response: %w", err)
		}
		return devices, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("unauthorized: the stored account token is invalid or expired; run `omnia cloud login` again")
	default:
		return nil, fmt.Errorf("list devices failed (%d): %s", resp.StatusCode, serverError(body))
	}
}

// resolveDeviceIDByName maps a friendly device name to its id. Device names are
// unique per account (UNIQUE(account_id, name)).
func resolveDeviceIDByName(devices []cloudDeviceInfo, name string) (string, error) {
	name = strings.TrimSpace(name)
	for _, d := range devices {
		if d.Name == name {
			return d.ID, nil
		}
	}
	available := make([]string, 0, len(devices))
	for _, d := range devices {
		available = append(available, d.Name)
	}
	if len(available) == 0 {
		return "", fmt.Errorf("no device named %q found (this account has no devices; log in with --device <name> first)", name)
	}
	return "", fmt.Errorf("no device named %q found; known devices: %s", name, strings.Join(available, ", "))
}

func cmdCloudDevicesList(cfg store.Config) {
	fs := flag.NewFlagSet("omnia cloud devices list", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	cloudAlias := fs.String("cloud", "", "cloud alias (default: default cloud)")
	if err := fs.Parse(os.Args[4:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	serverURL, token, err := resolveDeviceTarget(cfg, *server, strings.TrimSpace(*cloudAlias))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	devices, err := fetchDevices(serverURL, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	if len(devices) == 0 {
		fmt.Println("No devices registered for this account.")
		return
	}
	fmt.Printf("Devices (%d):\n", len(devices))
	for _, d := range devices {
		scope := "(unrestricted)"
		if len(d.ScopeProjects) > 0 {
			scope = strings.Join(d.ScopeProjects, ",")
		}
		lastSeen := d.LastSeenAt
		if strings.TrimSpace(lastSeen) == "" {
			lastSeen = "never"
		}
		fmt.Printf("  %-20s id=%-6s scope=%-24s last_seen=%s\n", d.Name, d.ID, scope, lastSeen)
	}
}

func cmdCloudDevicesScope(cfg store.Config) {
	args := os.Args[4:]
	device := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		device = strings.TrimSpace(args[0])
		args = args[1:]
	}
	fs := flag.NewFlagSet("omnia cloud devices scope", flag.ContinueOnError)
	projects := fs.String("projects", "", "comma-separated project list to restrict the device to (use --projects '' to make it unrestricted)")
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	cloudAlias := fs.String("cloud", "", "cloud alias (default: default cloud)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	if device == "" {
		fmt.Fprintln(os.Stderr, "usage: omnia cloud devices scope <device> --projects a,b,c")
		fmt.Fprintln(os.Stderr, "error: a device name is required")
		exitFunc(1)
		return
	}
	// Require --projects to be explicitly set so an accidental `scope <device>` never
	// silently clears an existing scope. Pass --projects '' to make it unrestricted.
	projectsSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "projects" {
			projectsSet = true
		}
	})
	if !projectsSet {
		fmt.Fprintln(os.Stderr, "usage: omnia cloud devices scope <device> --projects a,b,c")
		fmt.Fprintln(os.Stderr, "error: --projects is required (use --projects '' to make the device unrestricted)")
		exitFunc(1)
		return
	}

	serverURL, token, err := resolveDeviceTarget(cfg, *server, strings.TrimSpace(*cloudAlias))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	devices, err := fetchDevices(serverURL, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	id, err := resolveDeviceIDByName(devices, device)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	projectList := splitProjectsCSV(*projects)
	payload := struct {
		Projects []string `json:"projects"`
	}{Projects: projectList}

	resp, err := doAuthedCloudRequest(http.MethodPost, serverURL+"/devices/"+id+"/scope", token, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "set device scope request failed: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		if len(projectList) == 0 {
			fmt.Printf("Device %q is now unrestricted (no project scope).\n", device)
		} else {
			fmt.Printf("Device %q scoped to: %s\n", device, strings.Join(projectList, ", "))
		}
	case http.StatusUnauthorized:
		fmt.Fprintln(os.Stderr, "error: unauthorized; run `omnia cloud login` again")
		exitFunc(1)
	case http.StatusNotFound:
		fmt.Fprintf(os.Stderr, "error: device %q not found\n", device)
		exitFunc(1)
	default:
		fmt.Fprintf(os.Stderr, "set device scope failed (%d): %s\n", resp.StatusCode, serverError(body))
		exitFunc(1)
	}
}

func cmdCloudDevicesRevoke(cfg store.Config) {
	args := os.Args[4:]
	device := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		device = strings.TrimSpace(args[0])
		args = args[1:]
	}
	fs := flag.NewFlagSet("omnia cloud devices revoke", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	cloudAlias := fs.String("cloud", "", "cloud alias (default: default cloud)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	if device == "" {
		fmt.Fprintln(os.Stderr, "usage: omnia cloud devices revoke <device>")
		fmt.Fprintln(os.Stderr, "error: a device name is required")
		exitFunc(1)
		return
	}

	serverURL, token, err := resolveDeviceTarget(cfg, *server, strings.TrimSpace(*cloudAlias))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	devices, err := fetchDevices(serverURL, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}
	id, err := resolveDeviceIDByName(devices, device)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	resp, err := doAuthedCloudRequest(http.MethodDelete, serverURL+"/devices/"+id, token, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "revoke device request failed: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		fmt.Printf("Device %q revoked. Its project scope is denied immediately (fail-closed);\n", device)
		fmt.Println("any account token already issued for it stays valid until it expires or is refreshed.")
	case http.StatusUnauthorized:
		fmt.Fprintln(os.Stderr, "error: unauthorized; run `omnia cloud login` again")
		exitFunc(1)
	case http.StatusNotFound:
		fmt.Fprintf(os.Stderr, "error: device %q not found\n", device)
		exitFunc(1)
	default:
		fmt.Fprintf(os.Stderr, "revoke device failed (%d): %s\n", resp.StatusCode, serverError(body))
		exitFunc(1)
	}
}

// splitProjectsCSV splits a comma-separated project list, trimming whitespace and
// dropping empty entries. An empty or whitespace-only input yields an empty slice
// (unrestricted scope).
func splitProjectsCSV(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
