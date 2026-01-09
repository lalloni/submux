package integration

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Ensure signal handler is initialized
	initSignalHandler()

	// Run all tests
	code := m.Run()

	// Global cleanup after all tests
	fmt.Fprintln(os.Stderr, "\n=== Test suite finished, cleaning up all test clusters ===")
	cleanupAllClusters()

	os.Exit(code)
}
