package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/vvka-141/pgmi/internal/cli"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func main() {
	// Recover from panics to ensure graceful exits with stack traces
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic: %v\n%s\n", r, debug.Stack())
			os.Exit(pgmi.ExitPanic)
		}
	}()

	if os.Getenv("PGMI_TEST_PANIC") == "1" {
		panic("intentional test panic")
	}

	if err := cli.Execute(); err != nil {
		os.Exit(pgmi.ExitCodeForError(err))
	}
}
