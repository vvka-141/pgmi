package ui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestForcedApprover_ApprovesAfterCountdown(t *testing.T) {
	var output bytes.Buffer
	sleepCalls := 0

	approver := &ForcedApprover{
		output: &output,
		sleepFn: func(d time.Duration) {
			sleepCalls++
		},
	}

	approved, err := approver.RequestApproval(context.Background(), "testdb")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("Expected approval after countdown")
	}
	if sleepCalls != 5 {
		t.Errorf("Expected 5 sleep calls (one per second), got %d", sleepCalls)
	}
}

func TestForcedApprover_OutputContainsDbName(t *testing.T) {
	var output bytes.Buffer

	approver := &ForcedApprover{
		output:  &output,
		sleepFn: func(time.Duration) {},
	}

	_, _ = approver.RequestApproval(context.Background(), "my_production_db")

	out := output.String()
	if !strings.Contains(out, "my_production_db") {
		t.Errorf("Expected output to contain database name, got:\n%s", out)
	}
	if !strings.Contains(out, "DANGER") {
		t.Errorf("Expected output to contain DANGER warning, got:\n%s", out)
	}
	if !strings.Contains(out, "Proceeding with database overwrite") {
		t.Errorf("Expected output to contain proceeding message, got:\n%s", out)
	}
}

func TestForcedApprover_ContextCancellation(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	sleepCalls := 0
	approver := &ForcedApprover{
		output: &output,
		sleepFn: func(d time.Duration) {
			sleepCalls++
			if sleepCalls >= 2 {
				cancel()
			}
		},
	}

	approved, err := approver.RequestApproval(ctx, "testdb")
	if err == nil {
		t.Fatal("Expected context cancellation error")
	}
	if approved {
		t.Fatal("Expected approval to be false on cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("Expected context canceled error, got: %v", err)
	}
}

func TestForcedApprover_NewForcedApprover(t *testing.T) {
	approver := NewForcedApprover(true)
	if approver == nil {
		t.Fatal("Expected non-nil approver")
	}

	fa, ok := approver.(*ForcedApprover)
	if !ok {
		t.Fatal("Expected *ForcedApprover type")
	}
	if !fa.verbose {
		t.Error("Expected verbose=true")
	}
	if fa.output == nil {
		t.Error("Expected non-nil output writer")
	}
	if fa.sleepFn == nil {
		t.Error("Expected non-nil sleep function")
	}
}

func TestInteractiveApprover_MatchingInput(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("mydb\n")

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("Expected approval for matching input")
	}

	out := output.String()
	if !strings.Contains(out, "Confirmed") {
		t.Errorf("Expected confirmation message, got:\n%s", out)
	}
}

func TestInteractiveApprover_NonMatchingInput(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("wrong_name\n")

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if approved {
		t.Fatal("Expected denial for non-matching input")
	}

	out := output.String()
	if !strings.Contains(out, "does not match") {
		t.Errorf("Expected mismatch message, got:\n%s", out)
	}
	if !strings.Contains(out, "wrong_name") {
		t.Errorf("Expected output to echo user input, got:\n%s", out)
	}
}

func TestInteractiveApprover_EmptyInput(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("\n")

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if approved {
		t.Fatal("Expected denial for empty input")
	}
}

func TestInteractiveApprover_ReadError(t *testing.T) {
	var output bytes.Buffer
	input := &errorReader{err: io.ErrUnexpectedEOF}

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if err == nil {
		t.Fatal("Expected error for read failure")
	}
	if approved {
		t.Fatal("Expected denial on read error")
	}
	if !strings.Contains(err.Error(), "failed to read input") {
		t.Errorf("Expected read error wrapper, got: %v", err)
	}
}

func TestInteractiveApprover_ContextCancellation(t *testing.T) {
	var output bytes.Buffer
	input := newBlockingReader()
	t.Cleanup(func() { input.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(ctx, "mydb")
	if err == nil {
		t.Fatal("Expected context cancellation error")
	}
	if approved {
		t.Fatal("Expected denial on context cancellation")
	}
}

func TestInteractiveApprover_OutputContainsWarning(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("testdb\n")

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	_, _ = approver.RequestApproval(context.Background(), "testdb")

	out := output.String()
	if !strings.Contains(out, "WARNING") {
		t.Errorf("Expected WARNING in output, got:\n%s", out)
	}
	if !strings.Contains(out, "testdb") {
		t.Errorf("Expected database name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "permanently delete") {
		t.Errorf("Expected deletion warning, got:\n%s", out)
	}
}

func TestInteractiveApprover_InputWithWhitespace(t *testing.T) {
	var output bytes.Buffer
	input := strings.NewReader("  mydb  \n")

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("Expected approval for input with surrounding whitespace")
	}
}

func TestNewInteractiveApprover(t *testing.T) {
	approver := NewInteractiveApprover(false)
	if approver == nil {
		t.Fatal("Expected non-nil approver")
	}

	ia, ok := approver.(*InteractiveApprover)
	if !ok {
		t.Fatal("Expected *InteractiveApprover type")
	}
	if ia.verbose {
		t.Error("Expected verbose=false")
	}
	if ia.input == nil {
		t.Error("Expected non-nil input reader")
	}
	if ia.output == nil {
		t.Error("Expected non-nil output writer")
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type blockingReader struct {
	done chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{done: make(chan struct{})}
}

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.done
	return 0, io.EOF
}

func (r *blockingReader) Close() error {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return nil
}
