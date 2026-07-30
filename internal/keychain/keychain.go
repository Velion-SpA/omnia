// Package keychain accesses the OS keychain/keyring by shelling out to the
// platform CLI — macOS `/usr/bin/security`, Linux `secret-tool` (libsecret)
// — never a linked keychain library. This is the exact de-risk pattern
// already locked for git in internal/anchor (shell to the system binary
// instead of linking a cgo-dependent library), reused here so the
// memory-at-rest-security capability (design ADR-3) never breaks
// CGO_ENABLED=0: github.com/keybase/go-keychain and 99designs/keyring both
// link Security.framework via cgo on macOS.
//
// Degradation is load-bearing (spec REQ-434): callers MUST distinguish
// ErrUnavailable (no keychain CLI reachable — headless/Linux-without-
// libsecret/CI) from ErrNotFound (the CLI works, but no item is stored yet —
// the normal first-enable case) and treat every other error as a genuine
// failure. Only ErrUnavailable should trigger the encryption capability's
// fail-closed-by-default / explicit-opt-in-fallback decision (ADR-3); a
// generate-and-store on a NotFound is expected, ordinary behavior.
package keychain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ErrUnavailable is returned when no supported keychain CLI is reachable on
// this host (binary not found on PATH).
var ErrUnavailable = errors.New("keychain: no supported CLI found on PATH")

// ErrNotFound is returned when the keychain CLI is reachable but no item is
// stored yet under the requested service/account — the expected outcome the
// very first time a capability enables encryption.
var ErrNotFound = errors.New("keychain: item not found")

// runFunc executes `name args...` optionally piping stdin, returning combined
// stdout. Injectable (mirrors internal/anchor.Probe.runGit / defaultRunGit)
// so tests never spawn a real `security`/`secret-tool` process.
type runFunc func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

// Client resolves keychain items by shelling out to the platform CLI.
type Client struct {
	// run is injectable; defaults to defaultRun via New().
	run runFunc
	// goos selects which platform's CLI/argument shape to use. Defaults to
	// runtime.GOOS via New(); tests override it directly so both the macOS
	// and Linux code paths are exercised deterministically regardless of
	// the host actually running the test suite.
	goos string
}

// New constructs a Client backed by the real os/exec implementation for the
// current platform (runtime.GOOS).
func New() *Client {
	return &Client{run: defaultRun, goos: runtime.GOOS}
}

// exitError carries a failed CLI invocation's exit code and stderr so
// classification (unavailable vs not-found vs generic failure) never has to
// parse free-form error strings — mirrors internal/anchor's gitExitError.
type exitError struct {
	code   int
	stderr string
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exited %d: %s", e.code, strings.TrimSpace(e.stderr))
}

// defaultRun executes `name args...` via os/exec, optionally piping stdin,
// translating exec.ErrNotFound into a form classify() maps to ErrUnavailable.
func defaultRun(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &exitError{code: exitErr.ExitCode(), stderr: string(exitErr.Stderr)}
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return nil, err
	}
	return out, nil
}

// classify maps a run() error to ErrUnavailable, ErrNotFound, or a wrapped
// generic error, per-platform (macOS `security` uses exit code 44 for "item
// not found"; Linux `secret-tool lookup` uses exit code 1 with empty output
// — secret-tool's own documented not-found contract, distinct from its exit
// code 2 usage-error).
func classify(goos string, err error, stdoutEmpty bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, ErrUnavailable) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		switch goos {
		case "darwin":
			if ee.code == 44 {
				return fmt.Errorf("%w: %v", ErrNotFound, err)
			}
		default: // linux and anything else using secret-tool's contract
			if ee.code == 1 && stdoutEmpty {
				return fmt.Errorf("%w: %v", ErrNotFound, err)
			}
		}
	}
	return fmt.Errorf("keychain: %w", err)
}

// Get retrieves the secret stored under service/account. Returns ErrNotFound
// if the CLI is reachable but no item is stored yet, ErrUnavailable if no
// supported CLI is on PATH.
func (c *Client) Get(ctx context.Context, service, account string) (string, error) {
	name, args := c.getCommand(service, account)
	out, err := c.run(ctx, name, args, "")
	if err != nil {
		return "", classify(c.goos, err, len(out) == 0)
	}
	return strings.TrimSpace(string(out)), nil
}

// Set stores value under service/account, creating or updating the item.
// Returns ErrUnavailable if no supported CLI is on PATH.
func (c *Client) Set(ctx context.Context, service, account, value string) error {
	name, args, stdin := c.setCommand(service, account, value)
	_, err := c.run(ctx, name, args, stdin)
	if err != nil {
		return classify(c.goos, err, true)
	}
	return nil
}

func (c *Client) getCommand(service, account string) (name string, args []string) {
	if c.goos == "darwin" {
		return "security", []string{"find-generic-password", "-s", service, "-a", account, "-w"}
	}
	return "secret-tool", []string{"lookup", "service", service, "account", account}
}

func (c *Client) setCommand(service, account, value string) (name string, args []string, stdin string) {
	if c.goos == "darwin" {
		// -U updates the item in place if it already exists (upsert),
		// avoiding a spurious "already exists" failure on rotate-key.
		return "security", []string{"add-generic-password", "-U", "-s", service, "-a", account, "-w", value}, ""
	}
	// secret-tool store reads the secret from stdin when stdin is not a
	// terminal (its own documented convention) — never passed as an argv
	// entry, which would otherwise leak the secret via `ps`.
	label := service + " " + account
	return "secret-tool", []string{"store", "--label=" + label, "service", service, "account", account}, value
}

// GenerateKey returns 32 random bytes from crypto/rand, suitable as an
// adiantum encryption key (spec REQ-431: "no user-supplied passphrase").
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("keychain: generate key: %w", err)
	}
	return key, nil
}

// GetOrCreateHexKey resolves the hex-encoded key stored under service/account,
// generating and storing a new 32-byte crypto/rand key on the first call
// (spec REQ-431: "generated once on first enable ... no user-supplied
// passphrase"). created is true only when a new key was just generated.
//
// Any ErrUnavailable is propagated unchanged — callers (internal/store,
// internal/embed) MUST treat that as "cannot resolve a key" and apply their
// own fail-closed-by-default / explicit-opt-in-fallback decision (ADR-3);
// GetOrCreateHexKey itself never silently degrades.
func (c *Client) GetOrCreateHexKey(ctx context.Context, service, account string) (hexKey string, created bool, err error) {
	existing, err := c.Get(ctx, service, account)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", false, err
	}
	key, genErr := GenerateKey()
	if genErr != nil {
		return "", false, genErr
	}
	hexKey = hex.EncodeToString(key)
	if setErr := c.Set(ctx, service, account, hexKey); setErr != nil {
		return "", false, setErr
	}
	return hexKey, true, nil
}
