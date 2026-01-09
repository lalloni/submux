package submux

import "log/slog"

// invokeCallback safely invokes a callback function with panic recovery.
// The callback is invoked asynchronously in a separate goroutine.
func invokeCallback(logger *slog.Logger, callback MessageCallback, msg *Message) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("submux: panic in callback", "error", r)
			}
		}()
		callback(msg)
	}()
}
