package becs

import (
	"context"
	"fmt"
)

// FileRead retrieves a file from the BECS file repository by path.
// The path comes from batchvariables after a successful batch run
// (e.g. the value of "report_name"). Returns the file contents as a string.
// API: file param is a nested object {path}, data returns as base64Binary (NAG 3.29.1 §3.29, p.61).
func (c *Client) FileRead(ctx context.Context, path string) (string, error) {
	params := fileReadParams{
		Header: header{SessionID: c.SessionID},
		File:   fileReadFile{Path: path},
		Offset: 0,
		Length: 100000,
	}
	var result fileReadResult
	if err := c.call(ctx, "fileRead", params, &result); err != nil {
		return "", fmt.Errorf("fileRead %q: %w", path, err)
	}
	if result.Err != 0 {
		return "", fmt.Errorf("fileRead %q: BECS error %d: %s", path, result.Err, result.ErrTxt)
	}
	return string(result.Data), nil
}
