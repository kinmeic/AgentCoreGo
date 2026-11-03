package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Code is a stable, transport-friendly classification for service errors.
type Code string

const (
	CodeInvalidArgument  Code = "invalid_argument"
	CodeOutsideWorkspace Code = "outside_workspace"
	CodeNotFound         Code = "not_found"
	CodeAlreadyExists    Code = "already_exists"
	CodeConflict         Code = "revision_conflict"
	CodeNotDirectory     Code = "not_directory"
	CodeNotRegularFile   Code = "not_regular_file"
	CodeBinaryFile       Code = "binary_file"
	CodeTooLarge         Code = "too_large"
	CodeUnsupported      Code = "unsupported"
	CodeNotRepository    Code = "not_repository"
	CodeGit              Code = "git_error"
	CodeCanceled         Code = "canceled"
	CodeInternal         Code = "internal"
)

// Error is returned by every public Service operation. Err is retained for
// errors.Is/errors.As but is excluded from JSON serialization.
type Error struct {
	Code             Code   `json:"code"`
	Op               string `json:"op"`
	Path             string `json:"path,omitempty"`
	Message          string `json:"message"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
	CurrentRevision  string `json:"current_revision,omitempty"`
	Err              error  `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	where := e.Op
	if e.Path != "" {
		where += " " + e.Path
	}
	if where == "" {
		return e.Message
	}
	return where + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns a stable error code for err. Unknown errors are internal.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var serviceErr *Error
	if errors.As(err, &serviceErr) {
		return serviceErr.Code
	}
	return CodeInternal
}

func newError(op string, code Code, path, format string, args ...any) *Error {
	return &Error{Code: code, Op: op, Path: path, Message: fmt.Sprintf(format, args...)}
}

func wrapError(op string, code Code, path string, err error, format string, args ...any) *Error {
	return &Error{Code: code, Op: op, Path: path, Message: fmt.Sprintf(format, args...), Err: err}
}

func normalizeError(op string, err error) error {
	if err == nil {
		return nil
	}
	var serviceErr *Error
	if errors.As(err, &serviceErr) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return wrapError(op, CodeCanceled, "", err, "%v", err)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, os.ErrNotExist):
		return wrapError(op, CodeNotFound, "", err, "%v", err)
	default:
		return wrapError(op, CodeInternal, "", err, "%v", err)
	}
}
