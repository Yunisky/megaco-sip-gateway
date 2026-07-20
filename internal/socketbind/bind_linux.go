//go:build linux

package socketbind

import "syscall"

func validateDeviceBinding(string) error {
	return nil
}

func controlForDevice(device string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		if err := raw.Control(func(fd uintptr) {
			socketErr = syscall.SetsockoptString(
				int(fd),
				syscall.SOL_SOCKET,
				syscall.SO_BINDTODEVICE,
				device,
			)
		}); err != nil {
			return err
		}
		return socketErr
	}
}
