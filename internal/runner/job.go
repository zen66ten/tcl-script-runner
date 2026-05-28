package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"becs-runner/internal/becs"
)

// RunStatus is the outcome of a single Run.
type RunStatus string

const (
	RunFinished RunStatus = "finished" // batch reached StateFinished
	RunStopped  RunStatus = "stopped"  // batch reached StateStopped
	RunError    RunStatus = "error"    // tunnel/login/network failure before or after batch
)

// Run is the execution of one batch script against one environment.
type Run struct {
	Environment string
	StartedAt   time.Time
	FinishedAt  time.Time
	BatchID     int
	Status      RunStatus
	Output      string // fileRead content or lastlog text
	Err         string // set when Status == RunError
}

// Job is a single execution across one or more environments, serialised to
// jobs/<ID>.json (spec §4.1). ID is a UTC timestamp used as the filename.
type Job struct {
	ID          string
	StartedAt   time.Time
	FinishedAt  time.Time
	Script      string
	Variables   []becs.NameValue
	Runs        []Run
}

// NewJob creates a Job with a timestamp-based ID. Call Save when done.
func NewJob(script string, variables []becs.NameValue) *Job {
	now := time.Now().UTC()
	return &Job{
		ID:        now.Format("2006-01-02T15-04-05.000"),
		StartedAt: now,
		Script:    script,
		Variables: variables,
	}
}

// Save writes the Job as JSON to <dataDir>/jobs/<ID>.json atomically.
func (j *Job) Save(dataDir string) error {
	dir := filepath.Join(dataDir, "jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("job: mkdir %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("job: marshal: %w", err)
	}

	target := filepath.Join(dir, j.ID+".json")
	tmp, err := os.CreateTemp(dir, "job-*.json.tmp")
	if err != nil {
		return fmt.Errorf("job: create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("job: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("job: close temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("job: rename to %q: %w", target, err)
	}
	return nil
}

// LoadJob reads a single job by ID from <dataDir>/jobs/<id>.json.
func LoadJob(dataDir, id string) (*Job, error) {
	path := filepath.Join(dataDir, "jobs", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("job: read %q: %w", path, err)
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("job: parse %q: %w", path, err)
	}
	return &j, nil
}

// ListJobs loads all jobs from <dataDir>/jobs/, sorted newest first.
func ListJobs(dataDir string) ([]*Job, error) {
	dir := filepath.Join(dataDir, "jobs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("job: read dir %q: %w", dir, err)
	}

	var jobs []*Job
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		j, err := LoadJob(dataDir, id)
		if err != nil {
			continue // skip corrupted files silently
		}
		jobs = append(jobs, j)
	}

	sort.Slice(jobs, func(i, k int) bool {
		return jobs[i].StartedAt.After(jobs[k].StartedAt)
	})
	return jobs, nil
}
