package integration

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Ensure signal handler is initialized (also kills orphaned processes from previous runs)
	initSignalHandler()

	// Run all tests
	code := m.Run()

	// Global cleanup after all tests
	fmt.Fprintln(os.Stderr, "\n=== Test suite finished, cleaning up all test clusters ===")
	doCleanup()

	os.Exit(code)
}
