//go:build darwin

package main

/*
#include <stdint.h>
#include <stdlib.h>
*/
import "C"

import (
	"sync"
	"unsafe"

	"github.com/meanwebuser/whitetransport/core/mobile"
)

var lastError struct {
	sync.Mutex
	value string
}

func recordError(err error) C.int32_t {
	lastError.Lock()
	defer lastError.Unlock()
	if err == nil {
		lastError.value = ""
		return 0
	}
	lastError.value = err.Error()
	return -1
}

//export WTStartTun2Socks
func WTStartTun2Socks(fd C.int32_t, mtu C.int32_t, socksPort C.int32_t) C.int32_t {
	// Credentials never cross the extension ABI. The daemon listener is confirmed loopback-only.
	return recordError(mobile.StartTun2Socks(int64(fd), int64(mtu), int64(socksPort), "", ""))
}

//export WTStopTun2Socks
func WTStopTun2Socks() C.int32_t {
	return recordError(mobile.StopTun2Socks())
}

//export WTLastError
func WTLastError() *C.char {
	lastError.Lock()
	defer lastError.Unlock()
	return C.CString(lastError.value)
}

//export WTFreeCString
func WTFreeCString(value *C.char) {
	C.free(unsafe.Pointer(value))
}

func main() {}
