package fastconf

import "errors"

// Errors returned by the loader.
var (
	// ErrTenantNotFound is returned when a requested tenant is not present in
	// the loaded configuration.
	ErrTenantNotFound = errors.New("fastconf: tenant not found")

	// ErrClosed is returned after the loader has been closed.
	ErrClosed = errors.New("fastconf: loader is closed")

	// ErrNoRemoteURL is returned when a remote source is configured without a
	// URL.
	ErrNoRemoteURL = errors.New("fastconf: remote URL is empty")
)
