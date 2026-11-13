// Package spool is the file transport between `archwright tool` and the Works
// (ADR 0018). A call is a request file in the run's spool directory and a
// response file beside it; both are written whole (temp name, then linked into
// place) and never overwritten.
package spool

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// Environment variables the Works sets when it spawns a run.
const (
	EnvRun   = "ARCHWRIGHT_RUN"
	EnvSpool = "ARCHWRIGHT_SPOOL"
	EnvToken = "ARCHWRIGHT_RUN_TOKEN"
)

const (
	reqSuffix  = ".req.json"
	respSuffix = ".resp.json"
	// MaxRequestBytes bounds what the Works will read from an untrusted request file.
	MaxRequestBytes = 1 << 20
)

// Error codes carried in a failed Response.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeUnknownTool  = "unknown_tool"
	CodeInvalidInput = "invalid_input"
	CodeNotFound     = "not_found"
	CodeRunEnded     = "run_ended"
)

// Request is one tool call.
type Request struct {
	ID    string          `json:"id"`
	Token string          `json:"token"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Error is a rejected or failed call.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Response answers the Request with the same ID: Output on success, Error otherwise.
type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether id is safe to use in a file name.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// NewID returns a fresh request id.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "req-" + hex.EncodeToString(b)
}

// RequestPath and ResponsePath name a call's files inside a spool directory.
func RequestPath(dir, id string) string  { return filepath.Join(dir, id+reqSuffix) }
func ResponsePath(dir, id string) string { return filepath.Join(dir, id+respSuffix) }

// RequestID extracts the id from a request file name.
func RequestID(name string) (string, bool) {
	if len(name) <= len(reqSuffix) || name[len(name)-len(reqSuffix):] != reqSuffix {
		return "", false
	}
	id := name[:len(name)-len(reqSuffix)]
	return id, ValidID(id)
}

// writeNew writes v as JSON to path under a temporary name, then links it into
// place so it appears whole and an existing file is never replaced. It reports
// whether this call created the file.
func writeNew(dir, path string, v any) (bool, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// WriteRequest publishes a request unless one with its id already exists.
func WriteRequest(dir string, r Request) (bool, error) {
	return writeNew(dir, RequestPath(dir, r.ID), r)
}

// WriteResponse publishes a response unless one with its id already exists.
func WriteResponse(dir string, r Response) (bool, error) {
	return writeNew(dir, ResponsePath(dir, r.ID), r)
}

// ReadResponse returns the response for id, or fs.ErrNotExist.
func ReadResponse(dir, id string) (Response, error) {
	var r Response
	b, err := os.ReadFile(ResponsePath(dir, id))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// ReadRequest reads an untrusted request file: regular files only (no
// symlinks), bounded in size.
func ReadRequest(dir, id string) (Request, error) {
	var r Request
	path := RequestPath(dir, id)
	fi, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxRequestBytes {
		return r, errors.New("request is not a regular file of acceptable size")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// Remove deletes a call's request and response files, ignoring absent ones.
func Remove(dir, id string) {
	os.Remove(RequestPath(dir, id))
	os.Remove(ResponsePath(dir, id))
}
