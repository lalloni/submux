package submux

import "log"

// invokeCallback safely invokes a callback function with panic recovery.
// The callback is invoked asynchronously in a separate goroutine.
func invokeCallback(callback MessageCallback, msg *Message) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("submux: panic in callback: %v", r)
			}
		}()
		callback(msg)
	}()
}
