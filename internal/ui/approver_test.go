package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestForcedApprover_ApprovesAfterCountdown(t *testing.T) {
	var output bytes.Buffer
	sleepCalls := 0

	approver := &ForcedApprover{
		// The countdown is the interactive path; a non-interactive run skips it.
		interactive: true,
		output:      &output,
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
		// The countdown is the interactive path; a non-interactive run skips it.
		interactive: true,
		output:      &output,
		sleepFn:     func(time.Duration) {},
	}

	_, _ = approver.RequestApproval(context.Background(), "my_production_db")

	out := output.String()
	if !strings.Contains(out, "my_production_db") {
		t.Errorf("Expected output to contain database name, got:\n%s", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("Expected output to mention --force, got:\n%s", out)
	}
	if !strings.Contains(out, "DROP DATABASE") {
		t.Errorf("Expected output to contain DROP DATABASE warning, got:\n%s", out)
	}
	if !strings.Contains(out, "now.") {
		t.Errorf("Expected output to contain final 'now.' line, got:\n%s", out)
	}
}

func TestForcedApprover_ContextCancellation(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	sleepCalls := 0
	approver := &ForcedApprover{
		// The countdown is the interactive path; a non-interactive run skips it.
		interactive: true,
		output:      &output,
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
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got: %v", err)
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
	if fa.sleepFn != nil {
		t.Error("Expected nil sleepFn in production (ctx-aware select path)")
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
	if !strings.Contains(out, "DROP and RECREATE") {
		t.Errorf("Expected DROP and RECREATE warning, got:\n%s", out)
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

func TestForcedApprover_CtxCancelRespondsPromptly(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	approver := &ForcedApprover{
		// The countdown is the interactive path; a non-interactive run skips it.
		interactive: true,
		output:      &output,
		sleepFn:     nil,
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	approved, err := approver.RequestApproval(ctx, "testdb")
	elapsed := time.Since(start)

	if approved {
		t.Fatal("Expected denial on ctx cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Expected context.Canceled, got: %v", err)
	}
	// Sub-second responsiveness is the point: the countdown waits in one-second
	// ticks, and a select checked only at the end of each tick would swallow
	// Ctrl-C for up to a second. So the bound must stay under a second — it
	// cannot be widened to buy margin. The margin comes from cancelling early
	// instead: 20ms expected against a 500ms bound is 25x, where the original
	// 200ms was 2.5x and is the ratio that has flaked elsewhere in this repo.
	if elapsed > 500*time.Millisecond {
		t.Errorf("ctx cancel took %v; the countdown must not swallow Ctrl-C for a whole tick", elapsed)
	}
}

func TestInteractiveApprover_EOFReturnsDenied(t *testing.T) {
	var output bytes.Buffer
	input := &errorReader{err: io.EOF}

	approver := &InteractiveApprover{
		input:  input,
		output: &output,
	}

	approved, err := approver.RequestApproval(context.Background(), "mydb")
	if approved {
		t.Fatal("Expected denial on EOF")
	}
	if !errors.Is(err, pgmi.ErrApprovalDenied) {
		t.Fatalf("Expected ErrApprovalDenied, got: %v", err)
	}
	if !strings.Contains(output.String(), "No input available") {
		t.Errorf("Expected guidance message, got: %s", output.String())
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

// TestForcedApprover_NonInteractiveSkipsTheCountdown pins defect 2 of PGMI-269.
// --force exists for CI, and CI is where output is a log file: the countdown's
// only purpose is giving a human time to press Ctrl-C, so with no human it was
// five seconds of latency per forced overwrite — billed against --timeout — and
// a single ~300-character line, because \r redraws nothing in a non-TTY sink.
func TestForcedApprover_NonInteractiveSkipsTheCountdown(t *testing.T) {
	var output strings.Builder
	slept := 0

	approver := &ForcedApprover{
		output:      &output,
		interactive: false,
		sleepFn:     func(time.Duration) { slept++ },
	}

	start := time.Now()
	approved, err := approver.RequestApproval(context.Background(), "clirev")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approved {
		t.Error("--force must still approve without a terminal; it is what CI uses")
	}
	if slept != 0 {
		t.Errorf("the countdown slept %d time(s) with no terminal to watch it", slept)
	}
	if elapsed > time.Second {
		t.Errorf("a forced overwrite took %v with no terminal; the wait is pure latency there", elapsed)
	}

	out := output.String()
	if strings.Contains(out, "\r") {
		t.Errorf("carriage returns reached a non-TTY sink, producing one unreadable line:\n%q", out)
	}
	if strings.Contains(out, "Ctrl-C aborts") {
		t.Errorf("the countdown ran where nobody can interrupt it:\n%q", out)
	}
	// It still has to record the destructive act, on one readable line.
	if !strings.Contains(out, "clirev") || strings.Count(out, "\n") != 1 {
		t.Errorf("a non-interactive forced overwrite must log exactly one line naming the database, got:\n%q", out)
	}
}

// TestNonInteractiveApprover_RefusesImmediately pins defect 1. The interactive
// approver blocks on ReadString; against a CI runner's open-but-silent stdin
// that hung for the whole of --timeout and reported exit 16, "operation exceeded
// --timeout", for what was a missing --force.
func TestNonInteractiveApprover_RefusesImmediately(t *testing.T) {
	approver := NewNonInteractiveApprover()

	start := time.Now()
	approved, err := approver.RequestApproval(context.Background(), "clirev")
	elapsed := time.Since(start)

	if approved {
		t.Fatal("a destructive overwrite was approved with nobody to confirm it")
	}
	if err == nil {
		t.Fatal("expected a refusal error")
	}
	if elapsed > time.Second {
		t.Errorf("the refusal took %v; it must not wait on input that will never come", elapsed)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitApprovalDenied {
		t.Errorf("exit code %d, want %d (approval denied) — not %d, which reads as a slow database",
			got, pgmi.ExitApprovalDenied, pgmi.ExitTimeout)
	}
	// The message has to name the fix, since the operator's mistake is a flag.
	for _, want := range []string{"--force", "clirev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
