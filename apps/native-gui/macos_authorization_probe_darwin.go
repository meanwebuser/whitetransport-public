//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework WhiteTransportMacOS
char *WTMacAuthorizationProbeJSON(void);
void WTMacAuthorizationProbeFreeJSON(char *);
*/
import "C"

import (
	"encoding/json"
)

func probeMacAuthorization() macOSAuthorizationProbeResult {
	pointer := C.WTMacAuthorizationProbeJSON()
	if pointer == nil {
		return macOSAuthorizationProbeResult{Supported: true, Operation: "health", Error: "macOS bridge returned no probe result"}
	}
	defer C.WTMacAuthorizationProbeFreeJSON(pointer)
	var result macOSAuthorizationProbeResult
	if err := json.Unmarshal([]byte(C.GoString(pointer)), &result); err != nil {
		return macOSAuthorizationProbeResult{Supported: true, Operation: "health", Error: "decode macOS authorization probe: " + err.Error()}
	}
	return result
}
