package sessionssh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// openSSHProcess owns an sshd child and its completion notification.
type openSSHProcess struct {
	command *exec.Cmd
	done    chan error
}

// startOpenSSH starts sshd with platform-specific process ownership.
func startOpenSSH(path string, args []string, configPath string) (ManagedProcess, error) {
	command := exec.Command(path, args...)
	command.Dir = filepath.Dir(configPath)
	configureOpenSSH(command)
	// sshd runs at LogLevel ERROR; keep its bounded diagnostics in the daemon's
	// own managed log instead of silently discarding provisioning failures.
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &openSSHProcess{command: command, done: make(chan error, 1)}
	go func() {
		process.done <- command.Wait()
		close(process.done)
	}()
	return process, nil
}

// Done reports the sshd process exit result.
func (p *openSSHProcess) Done() <-chan error { return p.done }

// Stop delegates cleanup to the platform process implementation.
func (p *openSSHProcess) Stop(ctx context.Context) error {
	return stopOpenSSHProcess(ctx, p)
}
