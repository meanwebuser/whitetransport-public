//go:build darwin

package main

/*
#cgo LDFLAGS: -framework WhiteTransportMacOS
#include <stdlib.h>

char *WTSystemVPNPermission(void);
char *WTSystemVPNStart(const char *configurationJSON);
char *WTSystemVPNStop(void);
char *WTSystemVPNStatus(void);
char *WTSystemVPNLogs(void);
void WTSystemVPNFreeCString(char *value);
*/
import "C"

import (
	"fmt"
	"os"
	"time"
	"unsafe"
)

type darwinSystemVPNBridge struct{}

func newSystemVPNHost() systemVPNHost {
	if macOSVPNBackendFromEnv(os.Getenv("WT_MACOS_VPN_BACKEND")) == "networkextension" {
		return newNativeSystemVPNHost(darwinSystemVPNBridge{}, 15*time.Second)
	}
	return newDirectSystemVPNHost()
}

func (darwinSystemVPNBridge) Permission() (string, error) {
	return takeDarwinSystemVPNResponse(C.WTSystemVPNPermission())
}

func (darwinSystemVPNBridge) Start(profile string) (string, error) {
	configurationJSON := C.CString(profile)
	defer C.free(unsafe.Pointer(configurationJSON))
	return takeDarwinSystemVPNResponse(C.WTSystemVPNStart(configurationJSON))
}

func (darwinSystemVPNBridge) Stop() (string, error) {
	return takeDarwinSystemVPNResponse(C.WTSystemVPNStop())
}

func (darwinSystemVPNBridge) Status() (string, error) {
	return takeDarwinSystemVPNResponse(C.WTSystemVPNStatus())
}

func (darwinSystemVPNBridge) Logs() (string, error) {
	return takeDarwinSystemVPNResponse(C.WTSystemVPNLogs())
}

func takeDarwinSystemVPNResponse(value *C.char) (string, error) {
	if value == nil {
		return "", fmt.Errorf("native system VPN bridge returned a nil response")
	}
	defer C.WTSystemVPNFreeCString(value)
	return C.GoString(value), nil
}
