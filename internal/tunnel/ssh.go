package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh"

	"becs-runner/internal/config"
)

// SSH implements Tunnel via an SSH port-forward:
//
//	127.0.0.1:<local_port>  →  <ssh_host>  →  <remote_host>:<remote_port>
//
// The BECS client connects to 127.0.0.1:<local_port> after Connect returns.
// Caller is responsible for passing the decrypted password; it is ignored for
// key-based auth.
type SSH struct {
	cfg      config.SSHTunnelConfig
	password string // decrypted; only used when cfg.AuthMethod == "password"

	client   *ssh.Client
	listener net.Listener
}

// NewSSH creates an SSH tunnel. password must already be decrypted by the
// caller; it is ignored when cfg.AuthMethod is "key".
func NewSSH(cfg config.SSHTunnelConfig, password string) *SSH {
	return &SSH{cfg: cfg, password: password}
}

func (t *SSH) Connect(_ context.Context) error {
	auth, err := t.authMethods()
	if err != nil {
		return fmt.Errorf("ssh tunnel: auth: %w", err)
	}

	sshCfg := &ssh.ClientConfig{
		User: t.cfg.User,
		Auth: auth,
		// InsecureIgnoreHostKey is acceptable for v1 (operator-controlled hosts).
		// Phase 2: replace with known_hosts or per-env fingerprint check.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}

	jumpAddr := fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.Port)
	client, err := ssh.Dial("tcp", jumpAddr, sshCfg)
	if err != nil {
		return fmt.Errorf("ssh tunnel: dial %s: %w", jumpAddr, err)
	}
	t.client = client

	localAddr := fmt.Sprintf("127.0.0.1:%d", t.cfg.LocalPort)
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		client.Close()
		t.client = nil
		return fmt.Errorf("ssh tunnel: listen %s: %w", localAddr, err)
	}
	t.listener = ln

	go t.forward()
	return nil
}

func (t *SSH) Disconnect() error {
	var e1, e2 error
	if t.listener != nil {
		e1 = t.listener.Close()
		t.listener = nil
	}
	if t.client != nil {
		e2 = t.client.Close()
		t.client = nil
	}
	return errors.Join(e1, e2)
}

func (t *SSH) Dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil
}

func (t *SSH) authMethods() ([]ssh.AuthMethod, error) {
	switch t.cfg.AuthMethod {
	case "password":
		return []ssh.AuthMethod{ssh.Password(t.password)}, nil
	case "key":
		data, err := os.ReadFile(t.cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("reading key %q: %w", t.cfg.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parsing key %q: %w", t.cfg.KeyPath, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("unknown auth method %q", t.cfg.AuthMethod)
	}
}

// forward accepts local connections and proxies each through the SSH client to
// the BECS host as seen from the jump server.
func (t *SSH) forward() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return // listener closed by Disconnect
		}
		go t.handleConn(local)
	}
}

func (t *SSH) handleConn(local net.Conn) {
	defer local.Close()

	remoteAddr := fmt.Sprintf("%s:%d", t.cfg.RemoteHost, t.cfg.RemotePort)
	remote, err := t.client.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done
}
