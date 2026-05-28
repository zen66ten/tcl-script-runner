package tunnel

import (
	"context"
	"net"
)

// Tunnel abstracts a network tunnel that must be established before the BECS
// API endpoint is reachable (spec §3.2). Implementations: SSH, WireGuard.
type Tunnel interface {
	// Connect establishes the tunnel. Must succeed before any BECS API calls.
	Connect(ctx context.Context) error
	// Disconnect tears down the tunnel. Safe to call even if Connect failed.
	Disconnect() error
	// Dialer returns a custom DialContext for use in http.Transport.
	// SSH tunnels return nil (plain TCP to the bound local port is sufficient).
	// WireGuard tunnels return tnet.DialContext so only BECS traffic routes
	// through the tunnel.
	Dialer() func(ctx context.Context, network, addr string) (net.Conn, error)
}
