package becs

import "encoding/json"

// --- JSON-RPC 2.0 envelope ---

type request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	ID      int             `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- Common ---

// header is embedded in every authenticated call's params as "_header".
type header struct {
	SessionID string `json:"sessionid"`
}

// NameValue is the variable format for batchRun (confirmed working, spec Q1).
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// --- sessionLogin ---

type loginParams struct {
	Header   header `json:"_header"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResult struct {
	Err       int    `json:"err"`
	ErrTxt    string `json:"errtxt"`
	SessionID string `json:"sessionid"`
}

// --- sessionLogout ---

type logoutParams struct {
	Header header `json:"_header"`
}

type logoutResult struct {
	Err    int    `json:"err"`
	ErrTxt string `json:"errtxt"`
}

// --- batchRun ---

type batchRunParams struct {
	Header    header      `json:"_header"`
	Name      string      `json:"name"`
	Variables []NameValue `json:"variables,omitempty"`
	Timeout   int         `json:"timeout,omitempty"`
}

type batchRunResult struct {
	Err     int    `json:"err"`
	ErrTxt  string `json:"errtxt"`
	BatchID int    `json:"batchid"`
}

// --- batchInfoGet ---

type batchInfoGetParams struct {
	Header  header `json:"_header"`
	BatchID int    `json:"batchid"`
}

// BatchState values returned by batchInfoGet (spec §3.4.1).
type BatchState string

const (
	StateRunning  BatchState = "Running"
	StateFinished BatchState = "Finished"
	StateStopped  BatchState = "Stopped"
	StatePaused   BatchState = "Paused"
)

type batchInfoGetInfo struct {
	BatchID        int         `json:"batchid"`
	State          BatchState  `json:"state"`
	LastLog        string      `json:"lastlog"`
	Variables      []NameValue `json:"variables,omitempty"`      // input variables echoed back
	BatchVariables []NameValue `json:"batchvariables,omitempty"` // output variables set by the script, e.g. report_name
}

type batchInfoGetResult struct {
	Err    int              `json:"err"`
	ErrTxt string           `json:"errtxt"`
	Info   batchInfoGetInfo `json:"info"`
}

// --- batchList ---

type batchListParams struct {
	Header header `json:"_header"`
}

type batchListEntry struct {
	BatchID int        `json:"batchid"`
	Name    string     `json:"name"`
	State   BatchState `json:"state"`
}

type batchListResult struct {
	Err     int              `json:"err"`
	ErrTxt  string           `json:"errtxt"`
	Batches []batchListEntry `json:"batches,omitempty"`
}

// --- fileRead ---

type fileReadFile struct {
	Path string `json:"path"`
}

type fileReadParams struct {
	Header header      `json:"_header"`
	File   fileReadFile `json:"file"`
	Offset int         `json:"offset"`
	Length int         `json:"length"`
}

// fileReadResult.Data is base64Binary; encoding/json auto-decodes it to []byte.
type fileReadResult struct {
	Err    int    `json:"err"`
	ErrTxt string `json:"errtxt"`
	Data   []byte `json:"data"`
}
