//go:build windows

package sessionssh

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// configureOpenSSH intentionally avoids Unix-only process-group attributes.
func configureOpenSSH(_ *exec.Cmd) {
}

// stopOpenSSHProcess forcefully terminates the owned Windows sshd child.
func stopOpenSSHProcess(ctx context.Context, p *openSSHProcess) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
