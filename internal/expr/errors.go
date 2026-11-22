package expr

import (
	"fmt"
	"strings"
)

// CompileError reports a syntax error in one placeholder template.
//
// Offset is the zero-based byte offset of the offending character inside
// Source, which makes it possible to point at the exact position of a broken
// placeholder in a configuration file.
type CompileError struct {
	Source  string
	Offset  int
	Message string
}

// Error implements the error interface.
func (e *CompileError) Error() string {
	return fmt.Sprintf("placeholder template %q: offset %d: %s", e.Source, e.Offset, e.Message)
}

// PathError decorates a resolution error with the configuration path of the
// value that failed to resolve, for example "database.host" or
// "servers[0].port".
type PathError struct {
	Path string
	Err  error
}

// Error implements the error interface.
func (e *PathError) Error() string {
	return e.Path + ": " + e.Err.Error()
}

// Unwrap exposes the underlying resolution error.
func (e *PathError) Unwrap() error {
	return e.Err
}

// CycleError reports a cycle between placeholders, for example a value that
// references itself directly or through a chain of other values. Chain starts
// and ends with the same placeholder.
type CycleError struct {
	Chain []string
}

// Error implements the error interface.
func (e *CycleError) Error() string {
	return "placeholder cycle: " + strings.Join(e.Chain, " -> ")
}

// notFoundError reports a lookup that was neither in the document nor, for
// document resolution, in the environment.
type notFoundError struct {
	path string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("placeholder %q is not defined", e.path)
}

// compileErrorf builds a CompileError at offset.
func compileErrorf(source string, offset int, format string, args ...any) *CompileError {
	return &CompileError{
		Source:  source,
		Offset:  offset,
		Message: fmt.Sprintf(format, args...),
	}
}
