package gradientproto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// Execer runs commands over an established connection (an SSH client, in
// production). It exists so the derivation-reading and NAR-upload code in
// this package can be unit-tested without a real SSH server - see
// fakeExecer in the _test.go files.
type Execer interface {
	// Output runs cmd via the shell and returns its stdout. Returns an
	// error if the command exits non-zero, including stderr in the error
	// message for diagnosability.
	Output(ctx context.Context, cmd string) ([]byte, error)
	// StreamOutput runs cmd via the shell and returns a reader for its
	// stdout. The caller must fully read (or the returned closer must be
	// closed early) and then call wait to learn the final exit status -
	// wait blocks until the command has exited, so it must be called
	// after the reader is drained, not concurrently with reading it.
	StreamOutput(ctx context.Context, cmd string) (stdout io.ReadCloser, wait func() error, err error)
}

// sshExecer implements Execer over a *ssh.Client, opening one new SSH
// session per call - matching the pattern nixproto.ExecuteBuild and the
// rest of this codebase use (SSH sessions are cheap and short-lived; there
// is no session pooling anywhere in this codebase).
type sshExecer struct {
	client *ssh.Client
}

// NewSSHExecer wraps an established SSH client as an Execer.
func NewSSHExecer(client *ssh.Client) Execer {
	return &sshExecer{client: client}
}

func (e *sshExecer) Output(ctx context.Context, cmd string) ([]byte, error) {
	session, err := e.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("gradientproto: creating SSH session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("gradientproto: command %q failed: %w (stderr: %s)", cmd, err, stderr.String())
		}
		return stdout.Bytes(), nil
	case <-ctx.Done():
		session.Close() // unblocks session.Run with an error
		<-done
		return nil, ctx.Err()
	}
}

func (e *sshExecer) StreamOutput(ctx context.Context, cmd string) (io.ReadCloser, func() error, error) {
	session, err := e.client.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("gradientproto: creating SSH session: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, nil, fmt.Errorf("gradientproto: getting stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, nil, fmt.Errorf("gradientproto: starting command %q: %w", cmd, err)
	}

	// Close the session if ctx is cancelled while the caller is still
	// reading - mirrors nixproto.ExecuteBuild's cancellation pattern
	// (nixproto/proxy.go's context-watch goroutine around session.Start).
	sessionDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-sessionDone:
		}
	}()

	wait := func() error {
		close(sessionDone)
		err := session.Wait()
		session.Close()
		if err != nil {
			return fmt.Errorf("gradientproto: command %q failed: %w (stderr: %s)", cmd, err, stderr.String())
		}
		return nil
	}

	return io.NopCloser(stdout), wait, nil
}

// runJSON runs cmd (which must print exactly one JSON value to stdout) and
// unmarshals it into v.
func runJSON(ctx context.Context, exec Execer, cmd string, v any) error {
	out, err := exec.Output(ctx, cmd)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gradientproto: parsing JSON output of %q: %w", cmd, err)
	}
	return nil
}
