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

	term "github.com/charmbracelet/x/term"

	"github.com/velion/omnia/internal/store"
)

// resolveCloudServer returns the server URL to use for login/signup.
// Precedence: --server flag > cloud.json ServerURL > error.
func resolveCloudServer(flagServer string, cc *cloudConfig) (string, error) {
	if flagServer != "" {
		return strings.TrimRight(flagServer, "/"), nil
	}
	if cc != nil && cc.ServerURL != "" {
		return strings.TrimRight(cc.ServerURL, "/"), nil
	}
	return "", fmt.Errorf("no cloud server configured (run `engram cloud config --server <url>` or pass --server)")
}

// resolveCloudServerForAlias returns the server URL for a named cloud alias.
// Precedence: --server flag > named alias entry > default entry > error.
func resolveCloudServerForAlias(flagServer, alias string, v2 *cloudConfigV2) (string, error) {
	if flagServer != "" {
		return strings.TrimRight(flagServer, "/"), nil
	}
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
		if entry != nil && entry.ServerURL != "" {
			return strings.TrimRight(entry.ServerURL, "/"), nil
		}
	}
	return "", fmt.Errorf("no cloud server configured (run `engram cloud config --server <url>` or pass --server)")
}

// doCloudRequest sends a POST request with a JSON body and returns the
// HTTP response.  The caller is responsible for closing the body.
func doCloudRequest(method, url string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

// serverError extracts the "error" field from a non-2xx JSON response body.
// Falls back to the raw body if the field is absent or the body is not JSON.
func serverError(body []byte) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != "" {
		return envelope.Error
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return "(no error detail)"
}

// cmdCloudSignup implements `engram cloud signup`.
func cmdCloudSignup(cfg store.Config) {
	fs := flag.NewFlagSet("engram cloud signup", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	username := fs.String("username", "", "username for the new account")
	email := fs.String("email", "", "email address for the new account")
	password := fs.String("password", "", "password for the new account")
	cloudAlias := fs.String("cloud", "", "cloud alias to sign up against (default: default cloud)")

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

	if *username == "" || *email == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud signup --username <u> --email <e> --password <p> [--server <url>]")
		fmt.Fprintln(os.Stderr, "error: --username, --email, and --password are required")
		exitFunc(1)
		return
	}

	payload := struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}{Username: *username, Email: *email, Password: *password}

	resp, err := doCloudRequest(http.MethodPost, serverURL+"/auth/signup", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signup request failed: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		var created struct {
			ID       any    `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			fmt.Fprintf(os.Stderr, "signup succeeded but response could not be parsed: %v\n", err)
			exitFunc(1)
			return
		}
		fmt.Printf("Account created: id=%v username=%s email=%s\n", created.ID, created.Username, created.Email)
	case http.StatusConflict:
		fmt.Fprintln(os.Stderr, "error: username or email is already taken")
		exitFunc(1)
	default:
		fmt.Fprintf(os.Stderr, "signup failed (%d): %s\n", resp.StatusCode, serverError(body))
		exitFunc(1)
	}
}

// readPassword prompts for a password on stderr and reads it without echo.
// It is a var so tests can override it.
var readPasswordFn = func(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(os.Stderr) // newline after silent input
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// cmdCloudLogin implements `engram cloud login`.
func cmdCloudLogin(cfg store.Config) {
	fs := flag.NewFlagSet("engram cloud login", flag.ContinueOnError)
	server := fs.String("server", "", "cloud server URL (overrides cloud.json)")
	username := fs.String("username", "", "account username")
	password := fs.String("password", "", "account password (prompted if empty)")
	cloudAlias := fs.String("cloud", "", "cloud alias to login to (default: default cloud)")
	device := fs.String("device", "", "optional device name to bind the issued token to (restricts the token to the device's project scope)")

	if err := fs.Parse(os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	v2, _ := loadCloudConfigV2(cfg) // best-effort; nil is fine

	alias := strings.TrimSpace(*cloudAlias)
	// Validate the alias exists when explicitly specified.
	if alias != "" && v2 != nil {
		if _, ok := v2.getCloud(alias); !ok {
			fmt.Fprintf(os.Stderr, "error: cloud %q not found; run `engram cloud add %s --server <url>` first\n", alias, alias)
			exitFunc(1)
			return
		}
	}

	serverURL, err := resolveCloudServerForAlias(*server, alias, v2)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
		return
	}

	if *username == "" {
		fmt.Fprintln(os.Stderr, "usage: engram cloud login --username <u> [--password <p>] [--server <url>] [--cloud <alias>]")
		fmt.Fprintln(os.Stderr, "error: --username is required")
		exitFunc(1)
		return
	}

	pw := *password
	if pw == "" {
		pw, err = readPasswordFn("Password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading password: %v\n", err)
			exitFunc(1)
			return
		}
	}

	payload := struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Device   string `json:"device,omitempty"`
	}{Username: *username, Password: pw, Device: strings.TrimSpace(*device)}

	resp, err := doCloudRequest(http.MethodPost, serverURL+"/auth/login", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "login request failed: %v\n", err)
		exitFunc(1)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var result struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
			fmt.Fprintf(os.Stderr, "login succeeded but token missing in response: %v\n", err)
			exitFunc(1)
			return
		}
		// Persist token and username to the correct cloud entry.
		// The --server flag is used only for the HTTP request, not to update cloud.json,
		// so we pass "" for serverURL to preserve the existing stored server URL.
		if err := saveCloudConfigV2Entry(cfg, alias, "", result.Token, *username); err != nil {
			fmt.Fprintf(os.Stderr, "failed to save token to cloud.json: %v\n", err)
			exitFunc(1)
			return
		}
		fmt.Printf("Logged in as %s; account token stored in cloud.json\n", *username)
	case http.StatusUnauthorized:
		fmt.Fprintln(os.Stderr, "error: invalid credentials")
		exitFunc(1)
	default:
		fmt.Fprintf(os.Stderr, "login failed (%d): %s\n", resp.StatusCode, serverError(body))
		exitFunc(1)
	}
}
