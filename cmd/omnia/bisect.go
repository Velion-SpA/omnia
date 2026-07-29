package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/velion/omnia/internal/store"
)

const bisectStateVersion = 1
const bisectUsage = "usage: omnia bisect start --good T|ID --bad T|ID | good | bad | status | reset"

type bisectState struct {
	Version    int                 `json:"version"`
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
	if subcommand == "reset" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "Bisect state reset.", nil
	}
	if !cfg.TimeTravelEnabled {
		return "", errors.New("time travel is disabled; enable time_travel before bisecting")
	}
	s, err := store.New(cfg)
	if err != nil {
		return "", err
	}
	defer s.Close()
	if subcommand == "start" {
		if _, err := os.Stat(path); err == nil {
			return "", errors.New("bisect session already exists; run `omnia bisect reset` first")
		}
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
		events, err := s.BisectEvents(good, bad)
		if err != nil {
			return "", err
		}
		if len(events) == 0 {
			return "no revisions in range", nil
		}
		state := &bisectState{Version: bisectStateVersion, Candidates: events, Hi: len(events) - 1}
		if len(events) == 1 {
			return renderBisectEvent(s, events[0], "Implicated revision")
		}
		if err := writeBisectState(path, state); err != nil {
			return "", err
		}
		return runBisect(cfg, []string{"status"})
	}
	state, err := readBisectState(path)
	if err != nil {
		return "", err
	}
	prefix := ""
	for {
		mid := (state.Lo + state.Hi) / 2
		if _, err := s.BisectEventState(state.Candidates[mid]); err == store.ErrBisectEventTombstoned {
			prefix += fmt.Sprintf("Revision %d unavailable (tombstoned); skipping.\n", state.Candidates[mid].ID)
			state.Lo = mid + 1
			if state.Lo > state.Hi {
				_ = os.Remove(path)
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
			_ = os.Remove(path)
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

func readBisectState(path string) (*bisectState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, errors.New("no bisect session; run `omnia bisect start`")
	}
	var state bisectState
	if err != nil || json.Unmarshal(data, &state) != nil || state.Version != bisectStateVersion ||
		len(state.Candidates) == 0 || state.Lo < 0 || state.Hi < state.Lo || state.Hi >= len(state.Candidates) {
		return nil, errors.New("corrupt bisect state; run `omnia bisect reset`")
	}
	return &state, nil
}

func writeBisectState(path string, state *bisectState) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bisect-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(state)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
