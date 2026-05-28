package becs

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RunResult holds the outcome of a completed or stopped batch execution.
// The runner inspects State to decide success/failure, then uses
// BatchVariables (file path) or LastLog (summary text) to retrieve output.
type RunResult struct {
	State          BatchState
	LastLog        string
	BatchVariables []NameValue
}

// BatchEntry is one item from a batchList response.
type BatchEntry struct {
	BatchID int
	Name    string
	State   BatchState
}

// BatchRun starts a batch script on BECS and returns the assigned batch ID.
func (c *Client) BatchRun(ctx context.Context, name string, variables []NameValue, timeoutSecs int) (int, error) {
	params := batchRunParams{
		Header:    header{SessionID: c.SessionID},
		Name:      name,
		Variables: variables,
		Timeout:   timeoutSecs,
	}
	var result batchRunResult
	if err := c.call(ctx, "batchRun", params, &result); err != nil {
		return 0, fmt.Errorf("batchRun: %w", err)
	}
	if result.Err != 0 {
		return 0, fmt.Errorf("batchRun: BECS error %d: %s", result.Err, result.ErrTxt)
	}
	return result.BatchID, nil
}

// BatchInfoGet fetches the current status of a batch.
func (c *Client) BatchInfoGet(ctx context.Context, batchID int) (RunResult, error) {
	params := batchInfoGetParams{
		Header:  header{SessionID: c.SessionID},
		BatchID: batchID,
	}
	var result batchInfoGetResult
	if err := c.call(ctx, "batchInfoGet", params, &result); err != nil {
		return RunResult{}, fmt.Errorf("batchInfoGet: %w", err)
	}
	if result.Err != 0 {
		return RunResult{}, fmt.Errorf("batchInfoGet: BECS error %d: %s", result.Err, result.ErrTxt)
	}
	return RunResult{
		State:          result.Info.State,
		LastLog:        result.Info.LastLog,
		BatchVariables: result.Info.BatchVariables,
	}, nil
}

// Poll calls batchInfoGet in a loop until the batch reaches a terminal state
// (Finished or Stopped). It sleeps pollInterval between calls and respects
// context cancellation during the sleep.
func (c *Client) Poll(ctx context.Context, batchID int, pollInterval time.Duration) (RunResult, error) {
	for {
		result, err := c.BatchInfoGet(ctx, batchID)
		if err != nil {
			return RunResult{}, err
		}

		switch result.State {
		case StateFinished:
			return result, nil
		case StateStopped:
			slog.Warn("batch stopped", "batchid", batchID, "lastlog", result.LastLog)
			return result, nil
		case StatePaused:
			slog.Warn("batch paused, continuing to poll", "batchid", batchID)
		case StateRunning:
			// normal; wait and poll again
		}

		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// BatchList returns the list of existing batches on this BECS instance.
func (c *Client) BatchList(ctx context.Context) ([]BatchEntry, error) {
	params := batchListParams{Header: header{SessionID: c.SessionID}}
	var result batchListResult
	if err := c.call(ctx, "batchList", params, &result); err != nil {
		return nil, fmt.Errorf("batchList: %w", err)
	}
	if result.Err != 0 {
		return nil, fmt.Errorf("batchList: BECS error %d: %s", result.Err, result.ErrTxt)
	}
	entries := make([]BatchEntry, len(result.Batches))
	for i, b := range result.Batches {
		entries[i] = BatchEntry{BatchID: b.BatchID, Name: b.Name, State: b.State}
	}
	return entries, nil
}
