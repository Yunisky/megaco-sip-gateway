package socketbind

import (
	"context"
	"fmt"
	"net"
)

// ListenUDP creates a UDP socket and, when device is non-empty, binds the
// socket to that Linux interface before the local address is assigned. Binding
// to a VRF master makes both received packets and replies use the VRF table.
func ListenUDP(ctx context.Context, addr, device string) (*net.UDPConn, error) {
	if err := validateDeviceBinding(device); err != nil {
		return nil, err
	}

	lc := net.ListenConfig{}
	if device != "" {
		lc.Control = controlForDevice(device)
	}
	packetConn, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	udpConn, ok := packetConn.(*net.UDPConn)
	if !ok {
		_ = packetConn.Close()
		return nil, fmt.Errorf("listen %s returned %T, want *net.UDPConn", addr, packetConn)
	}
	return udpConn, nil
}
