package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/gofrs/flock"
	"github.com/natefinch/atomic"
	"github.com/velion/omnia/internal/store"
)

const bisectStateVersion = 1
const bisectUsage = "usage: omnia bisect start --good T|ID --bad T|ID | good | bad | status | reset"

var replaceBisectFile = atomic.ReplaceFile
var removeBisectFile = os.Remove
var beforeBisectMutationLock = func() {}
var newBisectGeneration = randomBisectGeneration

// Windows reports synthesized permissions; exact 0600 enforcement is POSIX-only.
var bisectPrivateModeRequired = func() bool { return runtime.GOOS != "windows" }

type bisectState struct {
	Version    int                 `json:"version"`
	Generation string              `json:"generation"`
	Candidates []store.BisectEvent `json:"candidates"`
	Lo         int                 `json:"lo"`
	Hi         int                 `json:"hi"`
}

func cmdBisect(cfg store.Config) {
	out, err := runBisect(cfg, os.Args[2:])
	if err != nil {
		fatal(err)
		return
	}
	fmt.Println(out)
}

func runBisect(cfg store.Config, args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New(bisectUsage)
	}
	subcommand := args[0]
	if subcommand == "help" || subcommand == "--help" || subcommand == "-h" {
		if len(args) != 1 {
			return "", errors.New("bisect help does not accept arguments")
		}
		return bisectUsage, nil
	}
	if subcommand != "start" && len(args) != 1 {
		return "", fmt.Errorf("bisect %s does not accept arguments", subcommand)
	}
	if subcommand != "start" && subcommand != "good" && subcommand != "bad" &&
		subcommand != "status" && subcommand != "reset" {
		return "", fmt.Errorf("unknown bisect subcommand %q", subcommand)
	}
	path := filepath.Join(cfg.DataDir, "bisect-state.json")
	info, dirErr := os.Lstat(cfg.DataDir)
	if os.IsNotExist(dirErr) {
		if subcommand == "reset" {
			return "Bisect state reset.", nil
		}
		if subcommand != "start" {
			return "", errors.New("bisect data directory does not exist")
		}
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return "", err
		}
		if bisectPrivateModeRequired() {
			if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
				return "", err
			}
		}
	} else if dirErr != nil {
		return "", dirErr
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("bisect data directory must be a directory, not a symlink")
	}
	var expected *bisectState
	if subcommand == "good" || subcommand == "bad" {
		var err error
		expected, err = readBisectState(path)
		if err != nil {
			return "", err
		}
		beforeBisectMutationLock()
	}
	lock := flock.New(filepath.Join(cfg.DataDir, "bisect-state.lock"), flock.SetPermissions(0o600))
	if err := lock.Lock(); err != nil {
		return "", err
	}
	defer lock.Close()
	if subcommand == "reset" {
		if err := removeBisectState(path); err != nil {
			return "", err
		}
		return "Bisect state reset.", nil
	}
	s, err := store.New(cfg)
	if err != nil {
		return "", err
	}
	defer s.Close()
	if !cfg.TimeTravelEnabled {
		return "", errors.New("time travel is disabled; enable time_travel before bisecting")
	}
	var state *bisectState
	if subcommand == "start" {
		good, bad := "", ""
		for i := 1; i < len(args); i++ {
			if (args[i] == "--good" || args[i] == "--bad") && i+1 < len(args) {
				i++
				if args[i-1] == "--good" {
					good = args[i]
				} else {
					bad = args[i]
				}
			} else {
				return "", fmt.Errorf("invalid bisect start argument %q", args[i])
			}
		}
		if good == "" {
			return "", errors.New("good bound is required")
		}
		if bad == "" {
			return "", errors.New("bad bound is required")
		}
		if err := ensureBisectStateAbsent(path); err != nil {
			return "", err
		}
		events, err := s.BisectEvents(good, bad)
		if err != nil {
			return "", err
		}
		if len(events) == 0 {
			return "no revisions in range", nil
		}
		generation, err := newBisectGeneration()
		if err != nil {
			return "", err
		}
		state = &bisectState{Version: bisectStateVersion, Generation: generation, Candidates: events, Hi: len(events) - 1}
		if len(events) == 1 {
			return renderBisectEvent(s, events[0], "Implicated revision")
		}
		if err := writeBisectState(path, state); err != nil {
			return "", err
		}
		subcommand = "status"
	} else {
		state, err = readBisectState(path)
		if err != nil {
			return "", err
		}
		if expected != nil {
			wantMid, gotMid := (expected.Lo+expected.Hi)/2, (state.Lo+state.Hi)/2
			if expected.Generation != state.Generation || expected.Lo != state.Lo || expected.Hi != state.Hi ||
				expected.Candidates[wantMid] != state.Candidates[gotMid] {
				return "", errors.New("stale bisect mark: candidate changed")
			}
		}
		for _, event := range state.Candidates {
			if _, eventErr := s.BisectEventState(event); eventErr != nil &&
				eventErr != store.ErrBisectEventTombstoned {
				return "", fmt.Errorf("bisect state stale: %w", eventErr)
			}
		}
	}
	prefix := ""
	for {
		mid := (state.Lo + state.Hi) / 2
		if _, err := s.BisectEventState(state.Candidates[mid]); err == store.ErrBisectEventTombstoned {
			if expected != nil {
				return "", errors.New("stale bisect mark: candidate unavailable")
			}
			prefix += fmt.Sprintf("Revision %d unavailable (tombstoned); skipping.\n", state.Candidates[mid].ID)
			state.Lo = mid + 1
			if state.Lo > state.Hi {
				if err := removeBisectState(path); err != nil {
					return "", err
				}
				return prefix + "no revisions remain", nil
			}
			if err := writeBisectState(path, state); err != nil {
				return "", err
			}
			continue
		} else if err != nil {
			return "", err
		}
		if subcommand == "good" {
			state.Lo = mid + 1
		} else if subcommand == "bad" {
			state.Hi = mid
		}
		if state.Lo > state.Hi {
			if err := removeBisectState(path); err != nil {
				return "", err
			}
			return prefix + "no revisions remain", nil
		}
		if subcommand != "status" {
			if err := writeBisectState(path, state); err != nil {
				return "", err
			}
			subcommand = "status"
			continue
		}
		if state.Lo == state.Hi {
			out, err := renderBisectEvent(s, state.Candidates[state.Lo], "Implicated revision")
			return prefix + out, err
		}
		out, err := renderBisectEvent(s, state.Candidates[mid], "Candidate")
		return fmt.Sprintf("%sBisecting: %d left to test\n%s", prefix, state.Hi-state.Lo+1, out), err
	}
}

func renderBisectEvent(s *store.Store, event store.BisectEvent, label string) (string, error) {
	obs, err := s.BisectEventState(event)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %d | %s | %s | %s | %s", label, obs.ID,
		compactBisectField(obs.Title, 80), compactBisectField(obs.Type, 32),
		compactBisectField(strings.SplitN(obs.Content, "\n", 2)[0], 96), compactBisectField(event.CreatedAt, 40)), nil
}

func compactBisectField(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return string(runes)
}

func validateBisectStateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("bisect state must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("bisect state must be a regular file")
	}
	if bisectPrivateModeRequired() && info.Mode().Perm() != 0o600 {
		return errors.New("bisect state must have mode 0600")
	}
	return nil
}

func validateBisectState(state *bisectState) error {
	if state.Version != bisectStateVersion || state.Generation == "" || len(state.Candidates) == 0 ||
		state.Lo < 0 || state.Hi < state.Lo || state.Hi >= len(state.Candidates) {
		return errors.New("invalid bisect state invariants")
	}
	return store.ValidateBisectEvents(state.Candidates)
}

func randomBisectGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func ensureBisectStateAbsent(path string) error {
	err := validateBisectStateFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("bisect session already exists; run `omnia bisect reset` first")
}

func readBisectState(path string) (*bisectState, error) {
	if err := validateBisectStateFile(path); err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("no bisect session; run `omnia bisect start`")
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	var state bisectState
	if err != nil || json.Unmarshal(data, &state) != nil {
		return nil, errors.New("corrupt bisect state; run `omnia bisect reset`")
	}
	if err := validateBisectState(&state); err != nil {
		return nil, fmt.Errorf("corrupt bisect state: %w", err)
	}
	return &state, nil
}

func syncBisectDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeBisectState(path string, state *bisectState) error {
	if err := validateBisectState(state); err != nil {
		return err
	}
	if err := validateBisectStateFile(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bisect-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceBisectFile(tmp.Name(), path); err != nil {
		return err
	}
	return syncBisectDirectory(path)
}

func removeBisectState(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := removeBisectFile(path); err != nil {
		return err
	}
	return syncBisectDirectory(path)
}
