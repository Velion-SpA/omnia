package keychain

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// fakeRunner lets tests script run() responses per call without spawning a
// real `security`/`secret-tool` process — mirrors internal/anchor/anchor_test.go's
// fakeGit pattern for Probe.runGit.
type fakeRunner struct {
	calls     []fakeCall
	responses []fakeResponse
}

type fakeCall struct {
	name  string
	args  []string
	stdin string
}

type fakeResponse struct {
	out []byte
	err error
}

func (f *fakeRunner) run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	if len(f.responses) == 0 {
		return nil, errors.New("fakeRunner: no more scripted responses")
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	return next.out, next.err
}

func newFakeClient(goos string, f *fakeRunner) *Client {
	return &Client{run: f.run, goos: goos}
}

// ─── 4.1 [RED]: Get/Set shell out with the right args on macOS ───

func TestGet_Darwin_ShellsOutToSecurityFindGenericPassword(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: []byte("deadbeef\n")}}}
	c := newFakeClient("darwin", f)

	got, err := c.Get(context.Background(), "omnia", "db-key-v1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("Get = %q, want %q", got, "deadbeef")
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 shell call, got %d", len(f.calls))
	}
	call := f.calls[0]
	if call.name != "security" {
		t.Errorf("name = %q, want %q", call.name, "security")
	}
	wantArgs := []string{"find-generic-password", "-s", "omnia", "-a", "db-key-v1", "-w"}
	if !equalArgs(call.args, wantArgs) {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}
}

func TestSet_Darwin_ShellsOutToSecurityAddGenericPassword(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: []byte("")}}}
	c := newFakeClient("darwin", f)

	if err := c.Set(context.Background(), "omnia", "db-key-v1", "deadbeef"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 shell call, got %d", len(f.calls))
	}
	call := f.calls[0]
	if call.name != "security" {
		t.Errorf("name = %q, want %q", call.name, "security")
	}
	wantArgs := []string{"add-generic-password", "-U", "-s", "omnia", "-a", "db-key-v1", "-w", "deadbeef"}
	if !equalArgs(call.args, wantArgs) {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}
}

// ─── 4.1 [RED]: Get/Set shell out with the right args on Linux ───

func TestGet_Linux_ShellsOutToSecretToolLookup(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: []byte("deadbeef\n")}}}
	c := newFakeClient("linux", f)

	got, err := c.Get(context.Background(), "omnia", "db-key-v1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("Get = %q, want %q", got, "deadbeef")
	}
	call := f.calls[0]
	if call.name != "secret-tool" {
		t.Errorf("name = %q, want %q", call.name, "secret-tool")
	}
	wantArgs := []string{"lookup", "service", "omnia", "account", "db-key-v1"}
	if !equalArgs(call.args, wantArgs) {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}
}

func TestSet_Linux_ShellsOutToSecretToolStoreWithStdin(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: []byte("")}}}
	c := newFakeClient("linux", f)

	if err := c.Set(context.Background(), "omnia", "db-key-v1", "deadbeef"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	call := f.calls[0]
	if call.name != "secret-tool" {
		t.Errorf("name = %q, want %q", call.name, "secret-tool")
	}
	wantArgs := []string{"store", "--label=omnia db-key-v1", "service", "omnia", "account", "db-key-v1"}
	if !equalArgs(call.args, wantArgs) {
		t.Errorf("args = %v, want %v", call.args, wantArgs)
	}
	if call.stdin != "deadbeef" {
		t.Errorf("stdin = %q, want %q", call.stdin, "deadbeef")
	}
}

// ─── 4.1 [RED]: a missing-CLI runner returns a typed "unavailable" error ───

func TestGet_MissingCLI_ReturnsErrUnavailable(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: nil, err: exec.ErrNotFound}}}
	c := newFakeClient("darwin", f)

	_, err := c.Get(context.Background(), "omnia", "db-key-v1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get error = %v, want ErrUnavailable", err)
	}
}

func TestSet_MissingCLI_ReturnsErrUnavailable(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: nil, err: exec.ErrNotFound}}}
	c := newFakeClient("linux", f)

	err := c.Set(context.Background(), "omnia", "db-key-v1", "deadbeef")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set error = %v, want ErrUnavailable", err)
	}
}

// ─── Not-found classification (distinct from Unavailable) ───

func TestGet_Darwin_ExitCode44_ReturnsErrNotFound(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: nil, err: &exitError{code: 44, stderr: "SecKeychainSearchCopyNext: The specified item could not be found in the keychain."}}}}
	c := newFakeClient("darwin", f)

	_, err := c.Get(context.Background(), "omnia", "db-key-v1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestGet_Linux_ExitCode1EmptyOutput_ReturnsErrNotFound(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: nil, err: &exitError{code: 1, stderr: ""}}}}
	c := newFakeClient("linux", f)

	_, err := c.Get(context.Background(), "omnia", "db-key-v1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

// ─── GenerateKey ───

func TestGenerateKey_Produces32RandomBytes(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(a) != 32 {
		t.Fatalf("len(key) = %d, want 32", len(a))
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if equalBytes(a, b) {
		t.Fatal("two GenerateKey() calls produced the same key — crypto/rand not used")
	}
}

// ─── GetOrCreateHexKey ───

func TestGetOrCreateHexKey_ExistingKey_ReturnsItWithoutGenerating(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: []byte("abc123\n")}}}
	c := newFakeClient("darwin", f)

	key, created, err := c.GetOrCreateHexKey(context.Background(), "omnia", "db-key-v1")
	if err != nil {
		t.Fatalf("GetOrCreateHexKey: %v", err)
	}
	if key != "abc123" {
		t.Errorf("key = %q, want %q", key, "abc123")
	}
	if created {
		t.Error("created = true, want false (key already existed)")
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected exactly 1 shell call (Get only), got %d: %+v", len(f.calls), f.calls)
	}
}

func TestGetOrCreateHexKey_NotFound_GeneratesAndStoresNewKey(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{
		{out: nil, err: &exitError{code: 44, stderr: "not found"}}, // Get: not found
		{out: []byte("")}, // Set: succeeds
	}}
	c := newFakeClient("darwin", f)

	key, created, err := c.GetOrCreateHexKey(context.Background(), "omnia", "db-key-v1")
	if err != nil {
		t.Fatalf("GetOrCreateHexKey: %v", err)
	}
	if !created {
		t.Error("created = false, want true (key did not exist)")
	}
	if len(key) != 64 { // 32 bytes hex-encoded
		t.Errorf("len(key) = %d, want 64 (hex-encoded 32 bytes)", len(key))
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 shell calls (Get + Set), got %d: %+v", len(f.calls), f.calls)
	}
	if f.calls[1].name != "security" || f.calls[1].args[0] != "add-generic-password" {
		t.Errorf("second call = %+v, want a Set (add-generic-password)", f.calls[1])
	}
}

func TestGetOrCreateHexKey_Unavailable_PropagatesErrUnavailable(t *testing.T) {
	f := &fakeRunner{responses: []fakeResponse{{out: nil, err: exec.ErrNotFound}}}
	c := newFakeClient("darwin", f)

	_, _, err := c.GetOrCreateHexKey(context.Background(), "omnia", "db-key-v1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetOrCreateHexKey error = %v, want ErrUnavailable", err)
	}
}

// ─── helpers ───

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
