// errors.go defines package-level sentinel errors for FFStream input management.

package ffstream

import "errors"

var (
	ErrInputNotFound = errors.New("no input at this (priority, num)")

	// ErrPipelineBusy is returned by hot-path RPC handlers (currently
	// InjectSubtitles) when the streammux input channel cannot accept
	// a packet within a bounded time. Indicates the downstream chain
	// is starved or stuck — typical cause is a silent / disconnected
	// source — and the caller should back off rather than retry
	// tightly. Without this fail-fast, a 1 Hz QML poller would stack
	// blocked goroutines until the gRPC HTTP/2 per-connection
	// concurrent-stream limit is hit and the connection appears
	// wedged from the client's perspective.
	ErrPipelineBusy = errors.New("pipeline busy: input channel full")
)
