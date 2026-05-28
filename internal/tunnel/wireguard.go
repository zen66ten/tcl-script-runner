package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"becs-runner/internal/config"
)

// WireGuard implements Tunnel using a userspace WireGuard tunnel (wireguard-go
// tun/netstack). No root, no kernel module, no system-wide routing changes.
// Only the BECS HTTP client uses the tunnel via Dialer(); all other traffic
// on the machine is unaffected.
type WireGuard struct {
	cfg        config.WireGuardConfig
	privateKey string // decrypted, base64-encoded
	psk        string // decrypted, base64-encoded; may be empty

	dev  *device.Device
	tnet *netstack.Net
}

// NewWireGuard creates a WireGuard tunnel. privateKey and psk must already be
// decrypted by the caller (base64-encoded WireGuard keys); psk may be empty.
func NewWireGuard(cfg config.WireGuardConfig, privateKey, psk string) *WireGuard {
	return &WireGuard{cfg: cfg, privateKey: privateKey, psk: psk}
}

func (t *WireGuard) Connect(_ context.Context) error {
	// Address may be CIDR notation (e.g. "192.168.99.10/32") — strip the prefix.
	localPrefix, err := netip.ParsePrefix(t.cfg.Address)
	if err != nil {
		// Try as a bare address too (no prefix).
		bare, err2 := netip.ParseAddr(t.cfg.Address)
		if err2 != nil {
			return fmt.Errorf("wireguard: local address %q: %w", t.cfg.Address, err)
		}
		localPrefix = netip.PrefixFrom(bare, bare.BitLen())
	}
	localAddr := localPrefix.Addr()

	dnsAddrs, err := t.parseDNS()
	if err != nil {
		return fmt.Errorf("wireguard: DNS: %w", err)
	}

	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{localAddr}, dnsAddrs, 1420)
	if err != nil {
		return fmt.Errorf("wireguard: create tun: %w", err)
	}

	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))

	ipc, err := t.buildIPC()
	if err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: build ipc: %w", err)
	}

	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: ipc set: %w", err)
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("wireguard: bring up: %w", err)
	}

	t.dev = dev
	t.tnet = tnet
	return nil
}

func (t *WireGuard) Disconnect() error {
	if t.dev != nil {
		t.dev.Down()
		t.dev.Close()
		t.dev = nil
		t.tnet = nil
	}
	return nil
}

// Dialer returns tnet.DialContext so the BECS http.Transport routes only BECS
// traffic through the WireGuard tunnel.
func (t *WireGuard) Dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if t.tnet == nil {
		return nil
	}
	return t.tnet.DialContext
}

// parseDNS returns DNS addresses from cfg.DNS (comma-separated), defaulting
// to Cloudflare 1.1.1.1 when the field is empty.
func (t *WireGuard) parseDNS() ([]netip.Addr, error) {
	if t.cfg.DNS == "" {
		a, _ := netip.ParseAddr("1.1.1.1")
		return []netip.Addr{a}, nil
	}
	var addrs []netip.Addr
	for _, s := range strings.Split(t.cfg.DNS, ",") {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", s, err)
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}

// buildIPC constructs the WireGuard UAPI IPC configuration string.
// Config keys are base64-encoded; IPC requires hex-encoded 32-byte keys.
func (t *WireGuard) buildIPC() (string, error) {
	privHex, err := b64toHex(t.privateKey)
	if err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	pubHex, err := b64toHex(t.cfg.PeerPublicKey)
	if err != nil {
		return "", fmt.Errorf("peer public key: %w", err)
	}

	var sb strings.Builder
	// Interface section
	sb.WriteString("private_key=" + privHex + "\n")
	// Peer section — public_key starts it
	sb.WriteString("public_key=" + pubHex + "\n")
	sb.WriteString("endpoint=" + t.cfg.Endpoint + "\n")
	for _, cidr := range strings.Split(t.cfg.AllowedIPs, ",") {
		sb.WriteString("allowed_ip=" + strings.TrimSpace(cidr) + "\n")
	}
	if t.psk != "" {
		pskHex, err := b64toHex(t.psk)
		if err != nil {
			return "", fmt.Errorf("preshared key: %w", err)
		}
		sb.WriteString("preshared_key=" + pskHex + "\n")
	}
	if t.cfg.PersistentKeepalive > 0 {
		fmt.Fprintf(&sb, "persistent_keepalive_interval=%d\n", t.cfg.PersistentKeepalive)
	}

	return sb.String(), nil
}

// b64toHex decodes a standard base64 key and returns its hex representation,
// as required by the WireGuard UAPI IPC protocol.
func b64toHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
