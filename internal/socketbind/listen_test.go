package socketbind

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

func TestListenUDPWithoutDevice(t *testing.T) {
	conn, err := ListenUDP(context.Background(), "127.0.0.1:0", "")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("test environment does not permit opening UDP sockets")
		}
		t.Fatalf("ListenUDP: %v", err)
	}
	defer conn.Close()

	if conn.LocalAddr() == nil {
		t.Fatal("missing local address")
	}
}
