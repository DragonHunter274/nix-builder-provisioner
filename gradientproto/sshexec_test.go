package gradientproto

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExecer is a test-only Execer that dispatches Output/StreamOutput
// calls to handler functions keyed by the exact command string, so tests
// can supply canned responses (typically real captured `nix ...` output -
// see testdata/nix-json/) without a live SSH connection.
type fakeExecer struct {
	t        *testing.T
	outputs  map[string]fakeOutputResult
	streams  map[string]fakeStreamResult
	commands []string // records every command Output/StreamOutput was called with
}

type fakeOutputResult struct {
	out []byte
	err error
}

type fakeStreamResult struct {
	data []byte
	err  error
}

func newFakeExecer(t *testing.T) *fakeExecer {
	return &fakeExecer{
		t:       t,
		outputs: make(map[string]fakeOutputResult),
		streams: make(map[string]fakeStreamResult),
	}
}

// onOutput registers a canned Output response for a command matched by
// substring (so tests don't need to replicate exact shell-quoting).
func (f *fakeExecer) onOutput(cmdSubstring string, out []byte, err error) {
	f.outputs[cmdSubstring] = fakeOutputResult{out: out, err: err}
}

func (f *fakeExecer) onOutputFile(cmdSubstring, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatalf("reading fixture %s: %v", path, err)
	}
	f.onOutput(cmdSubstring, data, nil)
}

func (f *fakeExecer) onStream(cmdSubstring string, data []byte, err error) {
	f.streams[cmdSubstring] = fakeStreamResult{data: data, err: err}
}

func (f *fakeExecer) Output(ctx context.Context, cmd string) ([]byte, error) {
	f.commands = append(f.commands, cmd)
	for substr, result := range f.outputs {
		if strings.Contains(cmd, substr) {
			return result.out, result.err
		}
	}
	f.t.Fatalf("fakeExecer.Output: no canned response for command: %s", cmd)
	return nil, fmt.Errorf("unreachable")
}

func (f *fakeExecer) StreamOutput(ctx context.Context, cmd string) (io.ReadCloser, func() error, error) {
	f.commands = append(f.commands, cmd)
	for substr, result := range f.streams {
		if strings.Contains(cmd, substr) {
			if result.err != nil {
				return nil, nil, result.err
			}
			r := io.NopCloser(strings.NewReader(string(result.data)))
			return r, func() error { return nil }, nil
		}
	}
	f.t.Fatalf("fakeExecer.StreamOutput: no canned response for command: %s", cmd)
	return nil, nil, fmt.Errorf("unreachable")
}

func nixJSONFixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", "nix-json", name)
}
