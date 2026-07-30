package scaffold_test

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	cmd := exec.Command(python, gatewayPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"DATABASE_URL=postgresql://stub:stub@127.0.0.1:1/stub",
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
		"PYTHONPATH="+workDir,
		// Without this, running python against the template tree drops
		// __pycache__ into it, which then breaks the template deployment test.
		"PYTHONDONTWRITEBYTECODE=1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn := dialWithRetry(t, addr)
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

func dialWithRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return conn
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("gateway did not start listening on %s", addr)
	return nil
}
