//go:build unix

package fdbased

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/xjasonlyu/tun2socks/v2/core/device"
)

const defaultMTU = 1500

type FD struct {
	stack.LinkEndpoint

	fd   int
	mtu  uint32
	file *os.File
	once sync.Once
}

func Open(name string, mtu uint32, offset int) (device.Device, error) {
	fd, err := strconv.Atoi(name)
	if err != nil {
		return nil, fmt.Errorf("cannot open fd: %s", name)
	}
	if mtu == 0 {
		mtu = defaultMTU
	}
	return open(fd, mtu, offset)
}

func (f *FD) Type() string {
	return Driver
}

func (f *FD) Name() string {
	return strconv.Itoa(f.fd)
}

func (f *FD) Close() {
	f.once.Do(func() {
		if f.file != nil {
			_ = f.file.Close()
			f.file = nil
			return
		}
		_ = unix.Close(f.fd)
	})
	f.LinkEndpoint.Close()
}

var _ device.Device = (*FD)(nil)
