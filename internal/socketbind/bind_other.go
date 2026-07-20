//go:build !linux

package socketbind

import (
	"fmt"
	"syscall"
)

func validateDeviceBinding(device string) error {
	if device == "" {
		return nil
	}
	return fmt.Errorf("bind_device %q requires Linux SO_BINDTODEVICE support", device)
}

func controlForDevice(string) func(string, string, syscall.RawConn) error {
	return nil
}
