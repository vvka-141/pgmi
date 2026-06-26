package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vvka-141/pgmi/internal/tui"
)

func defaultPager() string {
	if runtime.GOOS == "windows" {
		return "more"
	}
	return "less -R"
}

// PageWriter returns a writer that pages output when stdout is a TTY.
// When not interactive or PGMI_PAGER=cat, returns os.Stdout directly.
// The caller must call the returned close function when done writing.
func PageWriter() (io.Writer, func()) {
	if !tui.IsInteractive() {
		return os.Stdout, func() {}
	}

	pager := os.Getenv("PGMI_PAGER")
	if pager == "" {
		pager = os.Getenv("PAGER")
	}
	if pager == "" {
		pager = defaultPager()
	}

	if pager == "cat" {
		return os.Stdout, func() {}
	}

	parts := strings.Fields(pager)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	w, err := cmd.StdinPipe()
	if err != nil {
		return os.Stdout, func() {}
	}

	if err := cmd.Start(); err != nil {
		return os.Stdout, func() {}
	}

	return w, func() {
		w.Close()
		cmd.Wait()
	}
}

// Page writes content through the pager if stdout is a TTY.
func Page(content string) {
	w, close := PageWriter()
	defer close()
	fmt.Fprint(w, content)
}
