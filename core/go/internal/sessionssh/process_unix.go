//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package sessionssh

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// configureOpenSSH gives each Unix sshd lease a dedicated process group.
func configureOpenSSH(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopOpenSSHProcess terminates the complete sshd process group after ctx expires.
func stopOpenSSHProcess(ctx context.Context, p *openSSHProcess) error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	pid := p.command.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
		return ctx.Err()
	}
}
