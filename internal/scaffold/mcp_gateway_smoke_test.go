package scaffold_test

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The scaffolded MCP gateway assigned self.protocol_version, a reserved
// http.server attribute that BaseHTTPRequestHandler interpolates into the
// status line — every response shipped "2025-03-26 400 Bad Request" instead of
// "HTTP/1.1 400 Bad Request", so no HTTP client could parse it.
//
// This must speak raw TCP: any HTTP client library would either reject the
// response outright or normalize the status line, hiding the defect.
func TestMCPGateway_StatusLineIsHTTP(t *testing.T) {
	python := findPython(t)

	workDir := t.TempDir()

	writePsycopgStub(t, workDir)

	gateway, err := os.ReadFile(filepath.Join("templates", "advanced", "tools", "mcp-gateway.py"))
	if err != nil {
		t.Fatalf("read gateway source: %v", err)
	}
	gatewayPath := filepath.Join(workDir, "mcp-gateway.py")
	if err := os.WriteFile(gatewayPath, gateway, 0o600); err != nil {
		t.Fatalf("write gateway copy: %v", err)
	}

	port := freePort(t)

	gw := startGateway(t, python, gatewayPath, workDir, port)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn := gw.dial(t, addr)
	defer conn.Close()

	// Malformed JSON: the gateway answers 400 without touching the database.
	body := "not-json!"
	request := "POST /mcp HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"\r\n" + body

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	statusLine = strings.TrimRight(statusLine, "\r\n")

	if !strings.HasPrefix(statusLine, "HTTP/1.") {
		t.Fatalf("status line must start with the HTTP version, got %q — an MCP protocol version "+
			"here means a reserved http.server attribute was clobbered and no client can parse the response",
			statusLine)
	}

	// The 400 path returns before the version is recorded; it must not echo the
	// http.server default as if it were an MCP protocol version.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "MCP-Protocol-Version") {
			continue
		}
		if strings.Contains(strings.TrimSpace(value), "HTTP/") {
			t.Errorf("MCP-Protocol-Version header carries an HTTP version (%q); the guard is reading "+
				"the reserved attribute rather than a negotiated MCP version", strings.TrimSpace(value))
		}
	}
}

// writePsycopgStub drops a psycopg substitute into dir. The gateway resolves
// psycopg at import time, so booting it for a test that never reaches the
// database still needs the names it touches on the way up.
func writePsycopgStub(t *testing.T, dir string) {
	t.Helper()

	stub := `import enum


class _Err(Exception):
    pass


class errors:
    SerializationFailure = _Err
    DeadlockDetected = _Err
    OperationalError = _Err


class IsolationLevel(enum.IntEnum):
    READ_COMMITTED = 1
    REPEATABLE_READ = 2
    SERIALIZABLE = 3


def connect(*args, **kwargs):
    raise RuntimeError("stub psycopg: this test must not reach the database")
`
	if err := os.WriteFile(filepath.Join(dir, "psycopg.py"), []byte(stub), 0o600); err != nil {
		t.Fatalf("write psycopg stub: %v", err)
	}
}

func findPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("python not on PATH; skipping MCP gateway smoke test")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// gateway is the scaffolded MCP gateway running as a child process, holding
// everything needed to explain a failure to start.
//
// The previous version discarded the child's output and only ever reported
// "did not start listening", which is the symptom of every possible cause. On
// the macOS runner — where the sibling in-process test passes, so Python and
// the module import are both fine — that message left no way to tell a bind
// failure from a crash on the way up, and the job was left continue-on-error
// rather than diagnosed.
type gateway struct {
	out     *syncBuffer
	wait    chan error
	version string
	python  string
}

func startGateway(t *testing.T, python, script, dir string, port int) *gateway {
	t.Helper()

	out := &syncBuffer{}
	// -u as well as PYTHONUNBUFFERED: the macOS runner produced no output at
	// all, not even main()'s banner, which is printed before the server is
	// constructed. Either the child never reached main() or nothing it wrote
	// reached us, and the two need different fixes.
	cmd := exec.Command(python, "-u", script)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = append(os.Environ(),
		"DATABASE_URL=postgresql://stub:stub@127.0.0.1:1/stub",
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
		"PYTHONPATH="+dir,
		// Without this, running python against the template tree drops
		// __pycache__ into it, which then breaks the template deployment test.
		"PYTHONDONTWRITEBYTECODE=1",
		// Python buffers stdout when it is a pipe; without this a crash can
		// discard the very traceback that explains it.
		"PYTHONUNBUFFERED=1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-wait
	})

	version, err := exec.Command(python, "-V").CombinedOutput()
	if err != nil {
		version = fmt.Appendf(nil, "(%s -V failed: %v)", python, err)
	}
	return &gateway{out: out, wait: wait, version: strings.TrimSpace(string(version)), python: python}
}

// dial waits for the gateway to accept a connection, and stops early if it dies
// first rather than spending the whole timeout on a process that is already
// gone.
func (g *gateway) dial(t *testing.T, addr string) net.Conn {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-g.wait:
			g.wait <- err // hand it back so Cleanup's receive still completes
			t.Fatalf("the gateway exited before it listened on %s: %v%s", addr, err, g.report())
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return conn
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("gateway did not start listening on %s within 20s%s", addr, g.report())
	return nil
}

func (g *gateway) report() string {
	output := g.out.String()
	if strings.TrimSpace(output) == "" {
		// main() prints a banner before it constructs the server, so silence
		// means the child never got there — or that this runner is not giving
		// us its output at all. Ask a trivial child the same way to tell those
		// apart; without this the next run would be another guess.
		output = fmt.Sprintf("(nothing — and a control subprocess on this runner reported: %s)",
			captureSelfCheck(g.python))
	}
	return fmt.Sprintf("\npython: %s\n--- gateway output ---\n%s\n--- end ---", g.version, output)
}

// captureSelfCheck runs a child that certainly prints, through the same
// stdout/stderr path, and reports what came back.
func captureSelfCheck(python string) string {
	var buf syncBuffer
	probe := exec.Command(python, "-u", "-c",
		"import sys; print('stdout-reached-the-test'); sys.stderr.write('stderr-reached-the-test\\n')")
	probe.Stdout = &buf
	probe.Stderr = &buf
	if err := probe.Run(); err != nil {
		return fmt.Sprintf("the probe itself failed: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got == "" {
		return "nothing either — subprocess output is not reaching the test on this platform"
	}
	return strconv.Quote(got) + " — so capture works and the gateway really was silent"
}

// syncBuffer collects the child's stdout and stderr, which arrive on two
// goroutines and are read from a third.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
