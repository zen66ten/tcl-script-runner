package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zen66ten/tcl-script-runner/internal/becs"
	"github.com/zen66ten/tcl-script-runner/internal/config"
	"github.com/zen66ten/tcl-script-runner/internal/runner"
)

//go:embed templates
var templateFiles embed.FS

// App holds the dependencies shared across all HTTP handlers.
type App struct {
	cfg        *config.Config
	run        *runner.Runner
	dataDir    string
	passphrase string
	tmpls      map[string]*template.Template
	frag       *template.Template // layout-less fragments (api log, status)
}

// NewApp creates the App and pre-parses all templates.
func NewApp(cfg *config.Config, run *runner.Runner, dataDir, passphrase string) (*App, error) {
	a := &App{cfg: cfg, run: run, dataDir: dataDir, passphrase: passphrase}
	if err := a.loadTemplates(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *App) loadTemplates() error {
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"fmtDuration": func(start, end time.Time) string {
			if end.IsZero() {
				end = time.Now()
			}
			d := end.Sub(start).Truncate(time.Second)
			if d < time.Minute {
				return fmt.Sprintf("%ds", int(d.Seconds()))
			}
			return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
		},
		"inProgress": func(t time.Time) bool {
			return t.IsZero()
		},
		"snippet": func(s string) string {
			const max = 120
			if len(s) <= max {
				return s
			}
			return s[:max] + "…"
		},
		"statusIcon": func(s runner.RunStatus) string {
			switch s {
			case runner.RunFinished:
				return "✓"
			case runner.RunStopped:
				return "✗"
			case runner.RunError:
				return "!"
			}
			return "?"
		},
	}

	pages := []string{"dashboard", "environments", "environment_form", "job_log", "job_detail", "api_monitor"}
	a.tmpls = make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.New("").Funcs(funcs).ParseFS(templateFiles,
			"templates/layout.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return fmt.Errorf("parse template %q: %w", page, err)
		}
		a.tmpls[page] = t
	}

	frag, err := template.New("").Funcs(funcs).ParseFS(templateFiles, "templates/fragments.html")
	if err != nil {
		return fmt.Errorf("parse fragments: %w", err)
	}
	a.frag = frag
	return nil
}

func (a *App) render(w http.ResponseWriter, page string, data any) {
	t, ok := a.tmpls[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("render", "page", page, "err", err)
	}
}

// --- Dashboard ---

type dashData struct {
	Envs       []config.Environment
	RecentJobs []*runner.Job
	Busy       bool
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	jobs, _ := runner.ListJobs(a.dataDir)
	if len(jobs) > 10 {
		jobs = jobs[:10]
	}
	a.render(w, "dashboard", dashData{
		Envs:       a.cfg.Environments,
		RecentJobs: jobs,
		Busy:       a.run.IsBusy(),
	})
}

// --- Environments ---

type envListData struct {
	Envs []config.Environment
	Msg  string
}

func (a *App) listEnvironments(w http.ResponseWriter, r *http.Request) {
	a.render(w, "environments", envListData{
		Envs: a.cfg.Environments,
		Msg:  r.URL.Query().Get("msg"),
	})
}

type envFormData struct {
	Env   config.Environment
	IsNew bool
	Err   string
}

func (a *App) newEnvironmentForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, "environment_form", envFormData{
		Env:   config.NewEnvironment(),
		IsNew: true,
	})
}

func (a *App) createEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	env := parseEnvForm(r)

	becsPass := r.FormValue("password")
	if becsPass == "" {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: true, Err: "BECS password is required"})
		return
	}
	enc, err := config.Encrypt(becsPass, a.passphrase)
	if err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: true, Err: "encrypt password: " + err.Error()})
		return
	}
	env.Password = enc

	if err := a.encryptTunnelCreds(r, &env, config.Environment{}); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: true, Err: err.Error()})
		return
	}

	if err := a.cfg.Add(env); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: true, Err: err.Error()})
		return
	}
	if err := a.cfg.Save(); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: true, Err: "save config: " + err.Error()})
		return
	}
	http.Redirect(w, r, "/environments?msg=Environment+created", http.StatusSeeOther)
}

func (a *App) editEnvironmentForm(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env, _ := a.cfg.FindByName(name)
	if env == nil {
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}
	a.render(w, "environment_form", envFormData{Env: *env, IsNew: false})
}

func (a *App) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	oldName := r.PathValue("name")
	existing, _ := a.cfg.FindByName(oldName)
	if existing == nil {
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	env := parseEnvForm(r)

	// Keep existing encrypted password if field left blank
	env.Password, _ = a.maybeEncrypt(r.FormValue("password"), existing.Password)

	if err := a.encryptTunnelCreds(r, &env, *existing); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: false, Err: err.Error()})
		return
	}

	if err := a.cfg.Update(oldName, env); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: false, Err: err.Error()})
		return
	}
	if err := a.cfg.Save(); err != nil {
		a.render(w, "environment_form", envFormData{Env: env, IsNew: false, Err: "save config: " + err.Error()})
		return
	}
	http.Redirect(w, r, "/environments?msg=Environment+updated", http.StatusSeeOther)
}

func (a *App) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.cfg.Remove(name); err != nil {
		http.Redirect(w, r, "/environments?msg="+err.Error(), http.StatusSeeOther)
		return
	}
	if err := a.cfg.Save(); err != nil {
		slog.Error("save config after delete", "err", err)
	}
	http.Redirect(w, r, "/environments?msg=Environment+deleted", http.StatusSeeOther)
}

// --- Jobs ---

func (a *App) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := runner.ListJobs(a.dataDir)
	if err != nil {
		http.Error(w, "load jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, "job_log", struct{ Jobs []*runner.Job }{jobs})
}

func (a *App) jobDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := runner.LoadJob(a.dataDir, id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	a.render(w, "job_detail", struct{ Job *runner.Job }{job})
}

func (a *App) downloadRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env := r.PathValue("env")
	job, err := runner.LoadJob(a.dataDir, id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	var run *runner.Run
	for i := range job.Runs {
		if job.Runs[i].Environment == env {
			run = &job.Runs[i]
			break
		}
	}
	if run == nil || run.Output == "" {
		http.Error(w, "no output for this run", http.StatusNotFound)
		return
	}
	filename := env + "-" + id + ".txt"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write([]byte(run.Output))
}

// --- API Monitor ---

type apiLogView struct {
	TS, Dir, DirClass, Method, Endpoint, Body string
	ID                                        int
}

func toLogView(e becs.LogEntry) apiLogView {
	cls := "resp"
	switch e.Dir {
	case "→":
		cls = "req"
	case "✗":
		cls = "err"
	}
	return apiLogView{
		TS:       e.Time.Local().Format("15:04:05.000"),
		Dir:      e.Dir,
		DirClass: cls,
		Method:   e.Method,
		ID:       e.ID,
		Endpoint: e.Endpoint,
		Body:     prettyJSON(e.Body),
	}
}

func (a *App) apiMonitor(w http.ResponseWriter, r *http.Request) {
	a.render(w, "api_monitor", nil)
}

func (a *App) apiLogFragment(w http.ResponseWriter, r *http.Request) {
	entries := becs.Log.Entries()
	views := make([]apiLogView, 0, len(entries))
	for _, e := range entries {
		views = append(views, toLogView(e))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.frag.ExecuteTemplate(w, "apilog", views); err != nil {
		slog.Error("render apilog", "err", err)
	}
}

func (a *App) apiLogClear(w http.ResponseWriter, r *http.Request) {
	becs.Log.Clear()
	a.apiLogFragment(w, r)
}

func (a *App) statusFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.frag.ExecuteTemplate(w, "status", a.run.IsBusy()); err != nil {
		slog.Error("render status", "err", err)
	}
}

// --- Run ---

func (a *App) runJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	envNames := r.Form["env"]
	if len(envNames) == 0 {
		http.Redirect(w, r, "/?msg=No+environments+selected", http.StatusSeeOther)
		return
	}

	// svc_list is always sent (empty string is valid — script handles it).
	// parentoid is optional; omit if blank.
	vars := []becs.NameValue{
		{Name: "svc_list", Value: r.FormValue("svc_list")},
	}
	if p := r.FormValue("parentoid"); p != "" {
		vars = append(vars, becs.NameValue{Name: "parentoid", Value: p})
	}

	if a.run.IsBusy() {
		http.Redirect(w, r, "/?msg=busy", http.StatusSeeOther)
		return
	}

	// Use context.Background() — the job must complete even if the browser
	// navigates away or HTMX cancels the in-flight request mid-poll.
	job, err := a.run.RunJob(context.Background(), envNames, "batch_accounting.tcl", vars)
	if err != nil {
		slog.Error("run job", "err", err)
		http.Redirect(w, r, "/?msg=busy", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/jobs/"+job.ID, http.StatusSeeOther)
}

// --- Helpers ---

// parseEnvForm reads environment fields from the form, excluding passwords
// (those are handled separately to support keep-existing-if-blank behaviour).
func parseEnvForm(r *http.Request) config.Environment {
	env := config.Environment{
		Name:       strings.TrimSpace(r.FormValue("name")),
		BECSHost:   strings.TrimSpace(r.FormValue("becs_host")),
		BECSPort:   intField(r, "becs_port", 4499),
		Username:   strings.TrimSpace(r.FormValue("username")),
		TunnelType: config.TunnelType(r.FormValue("tunnel_type")),
		Enabled:    r.FormValue("enabled") == "on",
		Notes:      r.FormValue("notes"),
		Labels:     parseLabels(r),
		SSH: config.SSHTunnelConfig{
			Host:       r.FormValue("ssh_host"),
			Port:       intField(r, "ssh_port", 22),
			User:       r.FormValue("ssh_user"),
			AuthMethod: r.FormValue("ssh_auth_method"),
			KeyPath:    r.FormValue("ssh_key_path"),
			LocalPort:  intField(r, "local_port", 0),
			RemoteHost: r.FormValue("remote_host"),
			RemotePort: intField(r, "remote_port", 4499),
			JumpHost:   strings.TrimSpace(r.FormValue("ssh_jump_host")),
			JumpPort:   intField(r, "ssh_jump_port", 0),
			JumpUser:   strings.TrimSpace(r.FormValue("ssh_jump_user")),
		},
		WireGuard: config.WireGuardConfig{
			PeerPublicKey:       r.FormValue("wg_peer_public_key"),
			Endpoint:            r.FormValue("wg_endpoint"),
			AllowedIPs:          r.FormValue("wg_allowed_ips"),
			Address:             r.FormValue("wg_address"),
			DNS:                 r.FormValue("wg_dns"),
			PersistentKeepalive: intField(r, "wg_persistent_keepalive", 0),
		},
	}
	return env
}

// encryptTunnelCreds encrypts tunnel credentials from the form, keeping
// existing encrypted values when the form fields are left blank (edit mode).
func (a *App) encryptTunnelCreds(r *http.Request, env *config.Environment, existing config.Environment) error {
	var err error

	// SSH password
	env.SSH.Password, err = a.maybeEncrypt(r.FormValue("ssh_password"), existing.SSH.Password)
	if err != nil {
		return fmt.Errorf("encrypt SSH password: %w", err)
	}

	// SSH key passphrase (for passphrase-protected private keys)
	env.SSH.KeyPassphrase, err = a.maybeEncrypt(r.FormValue("ssh_key_passphrase"), existing.SSH.KeyPassphrase)
	if err != nil {
		return fmt.Errorf("encrypt SSH key passphrase: %w", err)
	}

	// WireGuard private key
	env.WireGuard.PrivateKey, err = a.maybeEncrypt(r.FormValue("wg_private_key"), existing.WireGuard.PrivateKey)
	if err != nil {
		return fmt.Errorf("encrypt WireGuard private key: %w", err)
	}

	// WireGuard PSK (optional)
	env.WireGuard.PresharedKey, err = a.maybeEncrypt(r.FormValue("wg_preshared_key"), existing.WireGuard.PresharedKey)
	if err != nil {
		return fmt.Errorf("encrypt WireGuard PSK: %w", err)
	}

	return nil
}

// maybeEncrypt encrypts newVal if non-empty; otherwise returns existing unchanged.
func (a *App) maybeEncrypt(newVal, existing string) (string, error) {
	if newVal == "" {
		return existing, nil
	}
	return config.Encrypt(newVal, a.passphrase)
}

// parseLabels reads parallel label_key / label_value form fields into a map,
// pairing them by submission order and skipping rows with an empty key.
// Returns nil if there are no labels so the YAML omitempty tag drops the key.
func parseLabels(r *http.Request) map[string]string {
	keys := r.Form["label_key"]
	vals := r.Form["label_value"]
	labels := make(map[string]string)
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v := ""
		if i < len(vals) {
			v = strings.TrimSpace(vals[i])
		}
		labels[k] = v
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// prettyJSON indents a compact JSON string for display; non-JSON is returned as-is.
func prettyJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}

func intField(r *http.Request, name string, def int) int {
	v, err := strconv.Atoi(r.FormValue(name))
	if err != nil || v == 0 {
		return def
	}
	return v
}
