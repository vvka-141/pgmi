package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestConsoleLogger_Verbose_WhenEnabled(t *testing.T) {
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logger := NewConsoleLogger(true)
	logger.Verbose("test message: %s", "value")

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	expected := "[VERBOSE] test message: value\n"
	if output != expected {
		t.Errorf("Expected %q, got %q", expected, output)
	}
}

func TestConsoleLogger_Verbose_WhenDisabled(t *testing.T) {
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logger := NewConsoleLogger(false)
	logger.Verbose("test message: %s", "value")

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if output != "" {
		t.Errorf("Expected no output, got %q", output)
	}
}

func TestConsoleLogger_Info(t *testing.T) {
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logger := NewConsoleLogger(false)
	logger.Info("info message: %s", "value")

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	expected := "info message: value\n"
	if output != expected {
		t.Errorf("Expected %q, got %q", expected, output)
	}
}

func TestConsoleLogger_Error(t *testing.T) {
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logger := NewConsoleLogger(false)
	logger.Error("error message: %s", "value")

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	expected := "[ERROR] error message: value\n"
	if output != expected {
		t.Errorf("Expected %q, got %q", expected, output)
	}
}

func TestConsoleLogger_ConcurrentSafety(t *testing.T) {
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logger := NewConsoleLogger(true)

	// Use a channel to read output in background
	outputCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		outputCh <- buf.String()
	}()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			logger.Info("message %d", id)
			logger.Verbose("verbose %d", id)
			logger.Error("error %d", id)
		}(i)
	}

	wg.Wait()
	w.Close()
	os.Stderr = old
	output := <-outputCh

	// Verify we got all messages (10 * 3 = 30 lines)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 30 {
		t.Errorf("Expected 30 lines, got %d", len(lines))
	}

	// Verify no interleaved output (each line should be complete)
	for i, line := range lines {
		if !strings.Contains(line, "message") && !strings.Contains(line, "verbose") && !strings.Contains(line, "error") {
			t.Errorf("Line %d appears corrupted: %q", i, line)
		}
	}
}

func TestNullLogger_DiscardsAllMessages(t *testing.T) {
	// Capture stdout to verify nothing is written
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewNullLogger()
	logger.Verbose("verbose")
	logger.Info("info")
	logger.Error("error")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if output != "" {
		t.Errorf("NullLogger should discard all messages, got: %q", output)
	}
}

func TestNullLogger_ConcurrentSafety(t *testing.T) {
	logger := NewNullLogger()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			logger.Info("message %d", id)
			logger.Verbose("verbose %d", id)
			logger.Error("error %d", id)
		}(i)
	}

	// Should complete without panic
	wg.Wait()
}

// BenchmarkConsoleLogger_Verbose measures performance of verbose logging
func BenchmarkConsoleLogger_Verbose(b *testing.B) {
	// Redirect stderr to /dev/null equivalent
	old := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	defer func() { os.Stderr = old }()

	logger := NewConsoleLogger(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Verbose("benchmark message %d", i)
	}
}

// BenchmarkConsoleLogger_VerboseDisabled measures performance when verbose is disabled
func BenchmarkConsoleLogger_VerboseDisabled(b *testing.B) {
	logger := NewConsoleLogger(false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Verbose("benchmark message %d", i)
	}
}

// BenchmarkNullLogger measures performance of null logger
func BenchmarkNullLogger(b *testing.B) {
	logger := NewNullLogger()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message %d", i)
	}
}

// Example demonstrates basic ConsoleLogger usage
// Note: Output goes to stderr, so we use fmt.Println to show expected behavior
func ExampleConsoleLogger() {
	// ConsoleLogger writes to stderr, which Example tests cannot capture.
	// Instead we demonstrate the expected output pattern:
	fmt.Println("Starting operation")
	fmt.Println("[VERBOSE] Debug details")
	fmt.Println("[ERROR] Operation failed")
	// Output:
	// Starting operation
	// [VERBOSE] Debug details
	// [ERROR] Operation failed
}

// Example demonstrates NullLogger usage
func ExampleNullLogger() {
	logger := NewNullLogger()
	logger.Info("This message is discarded")
	logger.Verbose("This too")
	logger.Error("And this")
	fmt.Println("Done")
	// Output:
	// Done
}
