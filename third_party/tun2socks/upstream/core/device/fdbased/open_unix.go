//go:build unix && !linux

package fdbased

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/xjasonlyu/tun2socks/v2/core/device"
	"github.com/xjasonlyu/tun2socks/v2/core/device/iobased"
)

func open(fd int, mtu uint32, offset int) (device.Device, error) {
	if fd < 0 {
		return nil, fmt.Errorf("invalid file descriptor: %d", fd)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("get flags for fd %d: %w", fd, err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		return nil, fmt.Errorf("set non-blocking mode for fd %d: %w", fd, err)
	}
	f := &FD{fd: fd, mtu: mtu}
	file := os.NewFile(uintptr(fd), f.Name())
	if file == nil {
		return nil, fmt.Errorf("create file for fd %d: os.NewFile returned nil", fd)
	}
	ep, err := iobased.New(file, mtu, offset)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("create endpoint: %w", err)
	}
	f.LinkEndpoint = ep
	f.file = file

	return f, nil
}
