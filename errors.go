package submux

import "errors"

var (
	// ErrInvalidClusterClient is returned when an invalid cluster client is provided.
	ErrInvalidClusterClient = errors.New("submux: invalid cluster client")

	// ErrInvalidChannel is returned when a channel name is invalid.
	ErrInvalidChannel = errors.New("submux: invalid channel name")

	// ErrSubscriptionFailed is returned when a subscription operation fails.
	ErrSubscriptionFailed = errors.New("submux: subscription failed")

	// ErrConnectionFailed is returned when a connection operation fails.
	ErrConnectionFailed = errors.New("submux: connection failed")

	// ErrClosed is returned when an operation is attempted on a closed SubMux.
	ErrClosed = errors.New("submux: SubMux is closed")
)
