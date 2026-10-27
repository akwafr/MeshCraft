package mdpv1

import (
	"context"
	"net"
)

// Dialer opens a TCP connection. `nil` = direct connections.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
