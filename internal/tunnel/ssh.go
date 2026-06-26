package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh"

	"github.com/zen66ten/tcl-script-runner/internal/config"
)

// SSH implements Tunnel via an SSH port-forward:
//
//	127.0.0.1:<local_port>  →  [jump_host →]  <ssh_host>  →  <remote_host>:<remote_port>
//
// When cfg.JumpHost is set, the tunnel first dials the jump host, then opens the
// connection to ssh_host through it (an OpenSSH "ProxyJump"). The same private
// key/passphrase is parsed once and reused for every hop, so a passphrase-protected
// .pem is only entered once.
//
// The BECS client connects to 127.0.0.1:<local_port> after Connect returns.
// Caller is responsible for passing the decrypted password and key passphrase;
// password is ignored for key-based auth, passphrase is ignored for password auth.
type SSH struct {
	cfg        config.SSHTunnelConfig
	password   string // decrypted; only used when cfg.AuthMethod == "password"
	passphrase string // decrypted key passphrase; only used when cfg.AuthMethod == "key"

	client   *ssh.Client // final hop (the host we forward from)
	jump     *ssh.Client // jump hop, if any; closed on Disconnect
	listener net.Listener
}

// NewSSH creates an SSH tunnel. password and passphrase must already be decrypted
// by the caller. password is ignored when cfg.AuthMethod is "key"; passphrase is
// ignored when cfg.AuthMethod is "password".
func NewSSH(cfg config.SSHTunnelConfig, password, passphrase string) *SSH {
	return &SSH{cfg: cfg, password: password, passphrase: passphrase}
}

func (t *SSH) Connect(_ context.Context) error {
	auth, err := t.authMethods()
	if err != nil {
		return fmt.Errorf("ssh tunnel: auth: %w", err)
	}

	hostAddr := fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.Port)
	hostCfg := &ssh.ClientConfig{
		User: t.cfg.User,
		Auth: auth,
		// InsecureIgnoreHostKey is acceptable for v1 (operator-controlled hosts).
		// Phase 2: replace with known_hosts or per-env fingerprint check.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}

	if t.cfg.JumpHost != "" {
		jumpUser := t.cfg.JumpUser
		if jumpUser == "" {
			jumpUser = t.cfg.User
		}
		jumpPort := t.cfg.JumpPort
		if jumpPort == 0 {
			jumpPort = 22
		}
		jumpAddr := fmt.Sprintf("%s:%d", t.cfg.JumpHost, jumpPort)
		jumpCfg := &ssh.ClientConfig{
			User:            jumpUser,
			Auth:            auth, // same key/passphrase reused for the jump
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		}
		jump, err := ssh.Dial("tcp", jumpAddr, jumpCfg)
		if err != nil {
			return fmt.Errorf("ssh tunnel: dial jump %s: %w", jumpAddr, err)
		}
		t.jump = jump

		// Open a connection to the final host *through* the jump, then run the
		// SSH handshake over it to get a real client for that host.
		conn, err := jump.Dial("tcp", hostAddr)
		if err != nil {
			t.closeClients()
			return fmt.Errorf("ssh tunnel: dial %s via jump: %w", hostAddr, err)
		}
		ncc, chans, reqs, err := ssh.NewClientConn(conn, hostAddr, hostCfg)
		if err != nil {
			conn.Close()
			t.closeClients()
			return fmt.Errorf("ssh tunnel: handshake %s via jump: %w", hostAddr, err)
		}
		t.client = ssh.NewClient(ncc, chans, reqs)
	} else {
		client, err := ssh.Dial("tcp", hostAddr, hostCfg)
		if err != nil {
			return fmt.Errorf("ssh tunnel: dial %s: %w", hostAddr, err)
		}
		t.client = client
	}

	localAddr := fmt.Sprintf("127.0.0.1:%d", t.cfg.LocalPort)
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		t.closeClients()
		return fmt.Errorf("ssh tunnel: listen %s: %w", localAddr, err)
	}
	t.listener = ln

	go t.forward()
	return nil
}

func (t *SSH) Disconnect() error {
	var e1 error
	if t.listener != nil {
		e1 = t.listener.Close()
		t.listener = nil
	}
	return errors.Join(e1, t.closeClients())
}

// closeClients closes the final and jump SSH clients (final first), tolerating
// nils so it is safe to call from any failure point in Connect.
func (t *SSH) closeClients() error {
	var e1, e2 error
	if t.client != nil {
		e1 = t.client.Close()
		t.client = nil
	}
	if t.jump != nil {
		e2 = t.jump.Close()
		t.jump = nil
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
		var signer ssh.Signer
		if t.passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(t.passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(data)
		}
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
