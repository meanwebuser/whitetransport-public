//go:build unix && !linux

package fdbased

import "testing"

func TestOpenRejectsInvalidFileDescriptor(t *testing.T) {
	device, err := Open("-1", 1500, 0)
	if err == nil {
		if device != nil {
			device.Close()
		}
		t.Fatal("Open(-1) returned nil error")
	}
	if device != nil {
		t.Fatalf("Open(-1) returned device with error: %v", err)
	}
}
