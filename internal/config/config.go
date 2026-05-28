package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// TunnelType enumerates supported tunnel modes (spec 3.1).
type TunnelType string

const (
	TunnelNone      TunnelType = "none"
	TunnelSSH       TunnelType = "ssh"
	TunnelWireGuard TunnelType = "wireguard"
)

// SSHTunnelConfig holds parameters for an SSH port-forward tunnel (spec 3.2.1).
// ssh_key_path is a path on the machine running becs-runner; use filepath when
// handling it to stay Windows-compatible.
type SSHTunnelConfig struct {
	Host       string `yaml:"ssh_host"`
	Port       int    `yaml:"ssh_port"`        // default 22
	User       string `yaml:"ssh_user"`
	AuthMethod string `yaml:"ssh_auth_method"` // "key" or "password"
	KeyPath    string `yaml:"ssh_key_path"`    // path to private key; filepath-safe
	Password   string `yaml:"ssh_password"`    // encrypted at rest; only used when AuthMethod == "password"
	LocalPort  int    `yaml:"local_port"`      // local port to bind for the forward
	RemoteHost string `yaml:"remote_host"`     // BECS host as seen from the jump host
	RemotePort int    `yaml:"remote_port"`     // default 4499
}

// WireGuardConfig holds parameters for a userspace WireGuard tunnel (spec 3.2.2).
// PrivateKey and PresharedKey are stored encrypted at rest.
type WireGuardConfig struct {
	PrivateKey          string `yaml:"wg_private_key"`
	PeerPublicKey       string `yaml:"wg_peer_public_key"`
	Endpoint            string `yaml:"wg_endpoint"`
	AllowedIPs          string `yaml:"wg_allowed_ips"`
	Address             string `yaml:"wg_address"`
	DNS                 string `yaml:"wg_dns,omitempty"`
	PresharedKey        string `yaml:"wg_preshared_key,omitempty"`
	PersistentKeepalive int    `yaml:"wg_persistent_keepalive,omitempty"`
}

// Environment represents a single BECS deployment (spec 3.1).
// Password is stored encrypted; callers must decrypt via crypto.Decrypt before use.
type Environment struct {
	Name       string          `yaml:"name"`
	BECSHost   string          `yaml:"becs_host"`
	BECSPort   int             `yaml:"becs_port"`
	Username   string          `yaml:"username"`
	Password   string          `yaml:"password"`    // AES-256-GCM encrypted, base64-encoded
	TunnelType TunnelType      `yaml:"tunnel_type"` // "none", "ssh", "wireguard"
	SSH        SSHTunnelConfig `yaml:"ssh_tunnel,omitempty"`
	WireGuard  WireGuardConfig `yaml:"wireguard,omitempty"`
	Enabled    bool            `yaml:"enabled"`
	Notes      string          `yaml:"notes,omitempty"`
}

// Config is the top-level structure serialized to/from config.yaml (spec 4.1).
type Config struct {
	PollIntervalSeconds   int           `yaml:"poll_interval_seconds"`
	DefaultTimeoutSeconds int           `yaml:"default_timeout_seconds"`
	Environments          []Environment `yaml:"environments"`

	// path is set on Load so Save knows where to write; not serialized.
	path string `yaml:"-"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PollIntervalSeconds:   5,
		DefaultTimeoutSeconds: 300,
		Environments:          []Environment{},
	}
}

// NewEnvironment returns an Environment with default port values filled in.
func NewEnvironment() Environment {
	return Environment{
		BECSPort:   4499,
		TunnelType: TunnelNone,
		Enabled:    true,
		SSH: SSHTunnelConfig{
			Port:       22,
			RemotePort: 4499,
		},
	}
}

// Load reads config.yaml from the given path. If the file does not exist,
// a default Config is returned so first-run works without a pre-created file.
func Load(path string) (*Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolving path %q: %w", path, err)
	}

	data, err := os.ReadFile(absPath)
	if errors.Is(err, os.ErrNotExist) {
		cfg := DefaultConfig()
		cfg.path = absPath
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: reading %q: %w", absPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parsing %q: %w", absPath, err)
	}

	cfg.path = absPath
	return &cfg, nil
}

// Save writes the Config back to the path it was loaded from.
// The file is written atomically via a temp file + rename.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config: no path set; load config first or set path explicitly")
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshaling: %w", err)
	}

	// Write to a temp file in the same directory so the rename is atomic
	// on both Windows and Linux.
	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, "config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("config: creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: renaming temp file to %q: %w", c.path, err)
	}

	return nil
}

// SetPath sets the file path used by Save. Useful when creating a new Config
// without loading from disk.
func (c *Config) SetPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("config: resolving path %q: %w", path, err)
	}
	c.path = absPath
	return nil
}

// --- CRUD ---

// FindByName returns a pointer to the Environment with the given name and its
// index in the slice, or nil / -1 if not found. The pointer is valid until the
// next Add/Remove/Save call.
func (c *Config) FindByName(name string) (*Environment, int) {
	for i := range c.Environments {
		if c.Environments[i].Name == name {
			return &c.Environments[i], i
		}
	}
	return nil, -1
}

// Add appends a new Environment. Returns an error if an environment with the
// same name already exists (name is the unique identifier per spec 3.1).
func (c *Config) Add(env Environment) error {
	if _, idx := c.FindByName(env.Name); idx != -1 {
		return fmt.Errorf("config: environment %q already exists", env.Name)
	}
	c.Environments = append(c.Environments, env)
	return nil
}

// Update replaces the Environment matching oldName with the new value.
// If the name is being changed, uniqueness of the new name is checked first.
func (c *Config) Update(oldName string, updated Environment) error {
	_, idx := c.FindByName(oldName)
	if idx == -1 {
		return fmt.Errorf("config: environment %q not found", oldName)
	}
	if updated.Name != oldName {
		if _, conflict := c.FindByName(updated.Name); conflict != -1 {
			return fmt.Errorf("config: environment %q already exists", updated.Name)
		}
	}
	c.Environments[idx] = updated
	return nil
}

// Remove deletes the Environment with the given name.
func (c *Config) Remove(name string) error {
	_, idx := c.FindByName(name)
	if idx == -1 {
		return fmt.Errorf("config: environment %q not found", name)
	}
	c.Environments = append(c.Environments[:idx], c.Environments[idx+1:]...)
	return nil
}
