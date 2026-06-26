package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/zen66ten/tcl-script-runner/internal/becs"
	"github.com/zen66ten/tcl-script-runner/internal/config"
	"github.com/zen66ten/tcl-script-runner/internal/tunnel"
)

// Runner orchestrates serial batch execution across environments (spec §3.4.3).
type Runner struct {
	cfg        *config.Config
	dataDir    string
	passphrase string
	mu         sync.Mutex
	busy       bool
}

// New creates a Runner. passphrase is used to decrypt credentials stored in config.
func New(cfg *config.Config, dataDir, passphrase string) *Runner {
	return &Runner{cfg: cfg, dataDir: dataDir, passphrase: passphrase}
}

// IsBusy reports whether a job is currently running.
func (r *Runner) IsBusy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// RunJob executes script against each named environment in order. Environments
// that fail (tunnel, login, batch error) are recorded as RunError and execution
// continues to the next one. The completed Job is saved to disk before returning.
func (r *Runner) RunJob(ctx context.Context, envNames []string, script string, variables []becs.NameValue) (*Job, error) {
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return nil, errors.New("a job is already running; wait for it to complete")
	}
	r.busy = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
	}()

	job := NewJob(script, variables)
	// Save immediately so the detail page loads while the job is in flight.
	if err := job.Save(r.dataDir); err != nil {
		slog.Warn("runner: initial job save", "err", err)
	}

	for _, name := range envNames {
		env, _ := r.cfg.FindByName(name)
		if env == nil {
			job.Runs = append(job.Runs, Run{
				Environment: name,
				StartedAt:   time.Now(),
				FinishedAt:  time.Now(),
				Status:      RunError,
				Err:         fmt.Sprintf("environment %q not found in config", name),
			})
		} else if !env.Enabled {
			slog.Info("skipping disabled environment", "env", name)
		} else {
			job.Runs = append(job.Runs, r.executeRun(ctx, env, script, variables))
		}
		if err := job.Save(r.dataDir); err != nil {
			slog.Warn("runner: mid-run job save", "err", err)
		}
	}

	job.FinishedAt = time.Now()
	if err := job.Save(r.dataDir); err != nil {
		return job, fmt.Errorf("runner: save job: %w", err)
	}
	return job, nil
}

// executeRun carries out the full lifecycle for one environment:
// tunnel → login → batchRun → poll → fetchOutput → logout → tunnel teardown.
func (r *Runner) executeRun(ctx context.Context, env *config.Environment, script string, variables []becs.NameValue) Run {
	run := Run{
		Environment: env.Name,
		StartedAt:   time.Now(),
	}

	becsPass, err := config.Decrypt(env.Password, r.passphrase)
	if err != nil {
		return r.fail(run, fmt.Errorf("decrypt BECS password: %w", err))
	}

	tun, err := r.buildTunnel(env)
	if err != nil {
		return r.fail(run, err)
	}
	if tun != nil {
		if err := tun.Connect(ctx); err != nil {
			return r.fail(run, fmt.Errorf("tunnel connect: %w", err))
		}
		defer func() {
			if err := tun.Disconnect(); err != nil {
				slog.Warn("tunnel disconnect", "env", env.Name, "err", err)
			}
		}()
	}

	// WireGuard supplies a custom dialer; SSH and direct use default TCP (nil).
	endpoint := fmt.Sprintf("http://%s:%d/", env.BECSHost, env.BECSPort)
	var transport http.RoundTripper
	if tun != nil {
		if d := tun.Dialer(); d != nil {
			transport = &http.Transport{DialContext: d}
		}
	}
	client := becs.NewClient(endpoint, transport)

	if err := client.Login(ctx, env.Username, becsPass); err != nil {
		return r.fail(run, fmt.Errorf("login: %w", err))
	}
	defer func() {
		if err := client.Logout(ctx); err != nil {
			slog.Warn("logout", "env", env.Name, "err", err)
		}
	}()

	pollInterval := time.Duration(r.cfg.PollIntervalSeconds) * time.Second
	batchID, err := client.BatchRun(ctx, script, variables, r.cfg.DefaultTimeoutSeconds)
	if err != nil {
		return r.fail(run, fmt.Errorf("batchRun: %w", err))
	}
	run.BatchID = batchID
	slog.Info("batch started", "env", env.Name, "batchid", batchID)

	result, err := client.Poll(ctx, batchID, pollInterval)
	if err != nil {
		return r.fail(run, fmt.Errorf("poll: %w", err))
	}

	output, err := r.fetchOutput(ctx, client, result)
	if err != nil {
		slog.Warn("fetch output failed, falling back to lastlog", "env", env.Name, "err", err)
		output = result.LastLog
	}

	run.FinishedAt = time.Now()
	run.Output = output
	if result.State == becs.StateFinished {
		run.Status = RunFinished
	} else {
		run.Status = RunStopped
	}
	return run
}

// buildTunnel constructs the appropriate Tunnel for the environment, decrypting
// credentials as needed. Returns nil for TunnelNone.
func (r *Runner) buildTunnel(env *config.Environment) (tunnel.Tunnel, error) {
	switch env.TunnelType {
	case config.TunnelNone:
		return nil, nil

	case config.TunnelSSH:
		sshPass, keyPassphrase := "", ""
		switch env.SSH.AuthMethod {
		case "password":
			var err error
			sshPass, err = config.Decrypt(env.SSH.Password, r.passphrase)
			if err != nil {
				return nil, fmt.Errorf("decrypt SSH password: %w", err)
			}
		case "key":
			if env.SSH.KeyPassphrase != "" {
				var err error
				keyPassphrase, err = config.Decrypt(env.SSH.KeyPassphrase, r.passphrase)
				if err != nil {
					return nil, fmt.Errorf("decrypt SSH key passphrase: %w", err)
				}
			}
		}
		return tunnel.NewSSH(env.SSH, sshPass, keyPassphrase), nil

	case config.TunnelWireGuard:
		privKey, err := config.Decrypt(env.WireGuard.PrivateKey, r.passphrase)
		if err != nil {
			return nil, fmt.Errorf("decrypt WireGuard private key: %w", err)
		}
		psk := ""
		if env.WireGuard.PresharedKey != "" {
			psk, err = config.Decrypt(env.WireGuard.PresharedKey, r.passphrase)
			if err != nil {
				return nil, fmt.Errorf("decrypt WireGuard PSK: %w", err)
			}
		}
		return tunnel.NewWireGuard(env.WireGuard, privKey, psk), nil

	default:
		return nil, fmt.Errorf("unknown tunnel type %q", env.TunnelType)
	}
}

// fetchOutput returns the batch result content. If batchvariables contains
// "report_name", the file is fetched via fileRead; otherwise lastlog is used.
func (r *Runner) fetchOutput(ctx context.Context, client *becs.Client, result becs.RunResult) (string, error) {
	for _, v := range result.BatchVariables {
		if v.Name == "report_name" && v.Value != "" {
			return client.FileRead(ctx, v.Value)
		}
	}
	return result.LastLog, nil
}

// fail stamps a run as RunError with the given error.
func (r *Runner) fail(run Run, err error) Run {
	slog.Error("run failed", "env", run.Environment, "err", err)
	run.FinishedAt = time.Now()
	run.Status = RunError
	run.Err = err.Error()
	return run
}
