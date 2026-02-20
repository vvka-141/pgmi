package wizards

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type mockTester struct {
	info   string
	err    error
	called bool
	gotCfg pgmi.ConnectionConfig
}

func (m *mockTester) TestConnection(_ context.Context, cfg pgmi.ConnectionConfig) (string, error) {
	m.called = true
	m.gotCfg = cfg
	return m.info, m.err
}

func drainCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, drainCmds(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

func findTestResult(msgs []tea.Msg) (testResultMsg, bool) {
	for _, msg := range msgs {
		if m, ok := msg.(testResultMsg); ok {
			return m, true
		}
	}
	return testResultMsg{}, false
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

func update(t *testing.T, m tea.Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	t.Helper()
	result, cmd := m.Update(msg)
	return result, cmd
}

func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	_, ok := msg.(tea.QuitMsg)
	return ok
}

func asWizard(t *testing.T, m tea.Model) ConnectionWizard {
	t.Helper()
	w, ok := m.(ConnectionWizard)
	if !ok {
		t.Fatalf("expected ConnectionWizard, got %T", m)
	}
	return w
}

func TestConnectionWizard_InitialState(t *testing.T) {
	w := NewConnectionWizard()
	if w.step != stepSelectProvider {
		t.Errorf("initial step = %d, want stepSelectProvider (%d)", w.step, stepSelectProvider)
	}
	if w.providerIdx != 0 {
		t.Errorf("initial providerIdx = %d, want 0", w.providerIdx)
	}
}

func TestConnectionWizard_SelectLocalProvider(t *testing.T) {
	w := NewConnectionWizard()

	// Select "Local / On-Premises" (first provider, already selected)
	m, _ := update(t, w, keyMsg("enter"))
	w = asWizard(t, m)

	// Local has only 1 auth method — should skip to input form
	if w.step != stepInputHost {
		t.Errorf("after selecting local provider, step = %d, want stepInputHost (%d)", w.step, stepInputHost)
	}
	if len(w.inputs) != 6 {
		t.Errorf("host form should have 6 inputs, got %d", len(w.inputs))
	}
}

func TestConnectionWizard_HostFormDefaults(t *testing.T) {
	w := NewConnectionWizard()

	// Select local provider
	m, _ := update(t, w, keyMsg("enter"))
	w = asWizard(t, m)

	// Check defaults
	if w.inputs[0].Value() != "localhost" {
		t.Errorf("host default = %q, want %q", w.inputs[0].Value(), "localhost")
	}
	if w.inputs[1].Value() != "5432" {
		t.Errorf("port default = %q, want %q", w.inputs[1].Value(), "5432")
	}
	if w.inputs[2].Value() != "" {
		t.Errorf("database should be empty (placeholder only), got %q", w.inputs[2].Value())
	}
	if w.inputs[3].Value() != "postgres" {
		t.Errorf("username default = %q, want %q", w.inputs[3].Value(), "postgres")
	}
}

func typeString(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, keyMsg(string(r)))
	}
	return m
}

func selectLocalAndFillDB(t *testing.T, w ConnectionWizard) (tea.Model, tea.Cmd) {
	t.Helper()
	// Select local provider → host form
	m, _ := update(t, w, keyMsg("enter"))
	// Enter on Host → Port
	m, _ = update(t, m, keyMsg("enter"))
	// Enter on Port → Database (focus index 2)
	m, _ = update(t, m, keyMsg("enter"))
	// Type database name
	m = typeString(t, m, "testdb")
	// Enter on Database → Management DB
	m, _ = update(t, m, keyMsg("enter"))
	// Enter on Management DB (default "postgres") → Username
	m, _ = update(t, m, keyMsg("enter"))
	// Enter on Username → Password
	m, _ = update(t, m, keyMsg("enter"))
	// Enter on Password → submit
	m, cmd := update(t, m, keyMsg("enter"))
	return m, cmd
}

func TestConnectionWizard_EnterAdvancesFields(t *testing.T) {
	w := NewConnectionWizard()

	// Select local provider → host form
	m, _ := update(t, w, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 0 {
		t.Fatalf("initial focus = %d, want 0", w.focusIndex)
	}

	// Enter on first field (Host) should advance to second (Port)
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 1 {
		t.Errorf("after Enter on host, focusIndex = %d, want 1", w.focusIndex)
	}
	if w.step != stepInputHost {
		t.Errorf("should still be on input step, got %d", w.step)
	}

	// Enter on Port → Database
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 2 {
		t.Errorf("after Enter on port, focusIndex = %d, want 2", w.focusIndex)
	}

	// Type database name (required, no default)
	m = typeString(t, m, "testdb")

	// Enter on Database → Management DB
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 3 {
		t.Errorf("after Enter on database, focusIndex = %d, want 3", w.focusIndex)
	}

	// Enter on Management DB → Username
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 4 {
		t.Errorf("after Enter on mgmt db, focusIndex = %d, want 4", w.focusIndex)
	}

	// Enter on Username → Password
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.focusIndex != 5 {
		t.Errorf("after Enter on username, focusIndex = %d, want 5", w.focusIndex)
	}

	// Enter on Password (last field) → should submit form
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.step != stepTestConnection {
		t.Errorf("after Enter on last field, step = %d, want stepTestConnection (%d)", w.step, stepTestConnection)
	}
	if !w.testing {
		t.Error("should be testing after form submit")
	}
}

func TestConnectionWizard_ValidationErrorShown(t *testing.T) {
	w := NewConnectionWizard()

	// Select local provider → host form
	m, _ := update(t, w, keyMsg("enter"))

	// Advance through all fields WITHOUT typing a database name
	for i := 0; i < 5; i++ {
		m, _ = update(t, m, keyMsg("enter"))
	}
	// Now on password (last field), press Enter → validation should fail
	m, _ = update(t, m, keyMsg("enter"))
	w = asWizard(t, m)

	if w.step == stepTestConnection {
		t.Fatal("should NOT advance to test connection with empty database")
	}
	if w.validationErr == "" {
		t.Fatal("validationErr should be set when database is empty")
	}
	if w.validationErr != "database name is required" {
		t.Errorf("validationErr = %q, want %q", w.validationErr, "database name is required")
	}

	// Typing clears the error
	m, _ = update(t, m, keyMsg("x"))
	w = asWizard(t, m)
	if w.validationErr != "" {
		t.Errorf("validationErr should be cleared after typing, got %q", w.validationErr)
	}
}

func TestConnectionWizard_TestSuccessThenQuit(t *testing.T) {
	w := NewConnectionWizard()

	m, _ := selectLocalAndFillDB(t, w)
	w = asWizard(t, m)
	if w.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", w.step)
	}

	// Simulate successful test result
	m, _ = update(t, m, testResultMsg{success: true, info: "PostgreSQL 16.1"})
	w = asWizard(t, m)
	if !w.testDone {
		t.Fatal("testDone should be true after testResultMsg")
	}
	if !w.testOK {
		t.Fatal("testOK should be true for success")
	}

	// Press Enter to confirm — should quit
	m, cmd := update(t, m, keyMsg("enter"))
	w = asWizard(t, m)

	if w.step != stepDone {
		t.Errorf("after Enter on success screen, step = %d, want stepDone (%d)", w.step, stepDone)
	}
	if !w.result.Tested {
		t.Error("result.Tested should be true")
	}
	if !isQuitCmd(cmd) {
		t.Error("expected tea.Quit command after confirming success")
	}
}

func TestConnectionWizard_TestFailureGoesBackToEdit(t *testing.T) {
	w := NewConnectionWizard()

	m, _ := selectLocalAndFillDB(t, w)

	// Simulate failed test
	m, _ = update(t, m, testResultMsg{success: false, err: fmt.Errorf("connection refused")})
	w = asWizard(t, m)
	if w.testOK {
		t.Fatal("testOK should be false for failure")
	}

	// Press Enter → should go back to edit form
	m, cmd := update(t, m, keyMsg("enter"))
	w = asWizard(t, m)
	if w.step != stepInputHost {
		t.Errorf("after Enter on failure, step = %d, want stepInputHost (%d)", w.step, stepInputHost)
	}
	if isQuitCmd(cmd) {
		t.Error("should NOT quit after test failure")
	}
}

func TestConnectionWizard_EscCancels(t *testing.T) {
	w := NewConnectionWizard()

	// Esc on provider selection → cancel
	m, cmd := update(t, w, keyMsg("esc"))
	w = asWizard(t, m)
	if !w.result.Cancelled {
		t.Error("Esc on provider selection should cancel")
	}
	if !isQuitCmd(cmd) {
		t.Error("expected tea.Quit command on cancel")
	}
}

func TestConnectionWizard_NavigateProviders(t *testing.T) {
	w := NewConnectionWizard()

	// Down → second provider
	m, _ := update(t, w, keyMsg("down"))
	w = asWizard(t, m)
	if w.providerIdx != 1 {
		t.Errorf("after down, providerIdx = %d, want 1", w.providerIdx)
	}

	// Up → back to first
	m, _ = update(t, m, keyMsg("up"))
	w = asWizard(t, m)
	if w.providerIdx != 0 {
		t.Errorf("after up, providerIdx = %d, want 0", w.providerIdx)
	}
}

func TestConnectionWizard_BuildConfigDefaults(t *testing.T) {
	w := NewConnectionWizard()

	m, _ := selectLocalAndFillDB(t, w)
	w = asWizard(t, m)

	cfg := w.result.Config
	if cfg.Host != "localhost" {
		t.Errorf("config.Host = %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Port != 5432 {
		t.Errorf("config.Port = %d, want 5432", cfg.Port)
	}
	if cfg.Database != "testdb" {
		t.Errorf("config.Database = %q, want %q", cfg.Database, "testdb")
	}
	if cfg.Username != "postgres" {
		t.Errorf("config.Username = %q, want %q", cfg.Username, "postgres")
	}
}

func TestConnectionWizard_FullHappyPath(t *testing.T) {
	w := NewConnectionWizard()

	// Step 1+2: Select local provider, fill database, submit
	m, _ := selectLocalAndFillDB(t, w)
	w = asWizard(t, m)
	if w.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", w.step)
	}

	// Step 3: Connection test succeeds
	m, _ = update(t, m, testResultMsg{success: true, info: "PostgreSQL 16.1"})
	w = asWizard(t, m)
	if !w.testDone || !w.testOK {
		t.Fatalf("step 3: expected test done and OK")
	}

	// Step 4: Press Enter to finish
	m, cmd := update(t, m, keyMsg("enter"))
	w = asWizard(t, m)

	// Verify final state
	if w.step != stepDone {
		t.Errorf("final step = %d, want stepDone (%d)", w.step, stepDone)
	}
	if !w.result.Tested {
		t.Error("result.Tested should be true")
	}
	if w.result.Cancelled {
		t.Error("result.Cancelled should be false")
	}
	if !isQuitCmd(cmd) {
		t.Error("expected tea.Quit as final command")
	}

	// Verify config
	cfg := w.result.Config
	if cfg.Host != "localhost" {
		t.Errorf("config.Host = %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Port != 5432 {
		t.Errorf("config.Port = %d, want 5432", cfg.Port)
	}
}

func TestConnectionWizard_MockTesterCalledOnSubmit(t *testing.T) {
	mock := &mockTester{info: "PostgreSQL 16.1"}
	w := NewConnectionWizard(WithTester(mock))

	m, cmd := selectLocalAndFillDB(t, w)
	wiz := asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg from cmd execution")
	}
	if !result.success {
		t.Errorf("expected success, got err: %v", result.err)
	}
	if result.info != "PostgreSQL 16.1" {
		t.Errorf("info = %q, want %q", result.info, "PostgreSQL 16.1")
	}
	if !mock.called {
		t.Error("mock tester should have been called")
	}
	if mock.gotCfg.Host != "localhost" {
		t.Errorf("mock got host = %q, want localhost", mock.gotCfg.Host)
	}
	if mock.gotCfg.Database != "postgres" {
		t.Errorf("mock got database = %q, want postgres (tests against management DB)", mock.gotCfg.Database)
	}
}

func TestConnectionWizard_MockTesterFailureFlow(t *testing.T) {
	mock := &mockTester{err: fmt.Errorf("connection refused")}
	w := NewConnectionWizard(WithTester(mock))

	m, cmd := selectLocalAndFillDB(t, w)

	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg")
	}
	if result.success {
		t.Error("expected failure")
	}

	m, _ = update(t, m, result)
	wiz := asWizard(t, m)
	if wiz.testOK {
		t.Error("testOK should be false")
	}

	m, cmd = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepInputHost {
		t.Errorf("step = %d, want stepInputHost", wiz.step)
	}
	if isQuitCmd(cmd) {
		t.Error("should not quit on failure")
	}
}

func TestConnectionWizard_EndToEndWithMockTester(t *testing.T) {
	mock := &mockTester{info: "PostgreSQL 16.1"}
	w := NewConnectionWizard(WithTester(mock))

	m, cmd := selectLocalAndFillDB(t, w)

	msgs := drainCmds(cmd)
	result, _ := findTestResult(msgs)
	m, _ = update(t, m, result)

	m, cmd = update(t, m, keyMsg("enter"))
	wiz := asWizard(t, m)

	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
	if !isQuitCmd(cmd) {
		t.Fatal("expected tea.Quit")
	}

	r := wiz.Result()
	if r.Cancelled {
		t.Error("should not be cancelled")
	}
	if !r.Tested {
		t.Error("should be tested")
	}
	if r.Config.Host != "localhost" {
		t.Errorf("host = %q, want localhost", r.Config.Host)
	}
	if r.Config.Port != 5432 {
		t.Errorf("port = %d, want 5432", r.Config.Port)
	}
	if r.Config.Database != "testdb" {
		t.Errorf("database = %q, want testdb", r.Config.Database)
	}
	if mock.gotCfg.Host != "localhost" {
		t.Errorf("mock got host = %q, want localhost", mock.gotCfg.Host)
	}
	if mock.gotCfg.Database != "postgres" {
		t.Errorf("mock got database = %q, want postgres (tests against management DB)", mock.gotCfg.Database)
	}
}

func TestConnectionWizard_AzureEntraIDFlow(t *testing.T) {
	mock := &mockTester{info: "Azure PostgreSQL ready"}
	w := NewConnectionWizard(WithTester(mock))

	m, _ := update(t, w, keyMsg("down"))
	m, _ = update(t, m, keyMsg("enter"))
	wiz := asWizard(t, m)
	if wiz.step != stepSelectAuth {
		t.Fatalf("expected stepSelectAuth, got %d", wiz.step)
	}

	m, _ = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepInputAzure {
		t.Fatalf("expected stepInputAzure, got %d", wiz.step)
	}
	if len(wiz.inputs) != 3 {
		t.Fatalf("Azure form should have 3 inputs, got %d", len(wiz.inputs))
	}

	m = typeString(t, m, "myserver.postgres.database.azure.com")
	m, _ = update(t, m, keyMsg("enter")) // server → database
	m = typeString(t, m, "testdb")
	m, _ = update(t, m, keyMsg("enter")) // database → username
	var cmd tea.Cmd
	m, cmd = update(t, m, keyMsg("enter")) // username → submit
	wiz = asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg")
	}

	m, _ = update(t, m, result)
	m, cmd = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
	if !isQuitCmd(cmd) {
		t.Error("expected tea.Quit")
	}
	if mock.gotCfg.AuthMethod != pgmi.AuthMethodAzureEntraID {
		t.Errorf("auth method = %v, want AzureEntraID", mock.gotCfg.AuthMethod)
	}
}

func TestConnectionWizard_RetryAfterFailure(t *testing.T) {
	failMock := &mockTester{err: fmt.Errorf("timeout")}
	w := NewConnectionWizard(WithTester(failMock))

	m, cmd := selectLocalAndFillDB(t, w)

	msgs := drainCmds(cmd)
	result, _ := findTestResult(msgs)
	m, _ = update(t, m, result)
	wiz := asWizard(t, m)
	if wiz.testOK {
		t.Fatal("first attempt should fail")
	}

	m, _ = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepInputHost {
		t.Fatalf("should return to input, got step %d", wiz.step)
	}

	// Now inject a success tester — simulate fixing the issue
	// Re-submit the form (inputs are recreated fresh, must type database again)
	wiz.tester = &mockTester{info: "PostgreSQL 16.1"}
	m = wiz
	m, _ = update(t, m, keyMsg("enter"))   // host
	m, _ = update(t, m, keyMsg("enter"))   // port
	m = typeString(t, m, "testdb")          // type database name
	m, _ = update(t, m, keyMsg("enter"))   // database
	m, _ = update(t, m, keyMsg("enter"))   // management db
	m, _ = update(t, m, keyMsg("enter"))   // username
	m, cmd = update(t, m, keyMsg("enter")) // password → submit
	wiz = asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	msgs = drainCmds(cmd)
	result, _ = findTestResult(msgs)
	if !result.success {
		t.Fatalf("second attempt should succeed, got err: %v", result.err)
	}

	m, _ = update(t, m, result)
	m, cmd = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
	if !isQuitCmd(cmd) {
		t.Error("expected tea.Quit")
	}
}

func asInitWizard(t *testing.T, m tea.Model) InitWizard {
	t.Helper()
	w, ok := m.(InitWizard)
	if !ok {
		t.Fatalf("expected InitWizard, got %T", m)
	}
	return w
}

func TestInitWizard_ConnectionEmbedded_SingleProgram(t *testing.T) {
	templates := DefaultTemplates()
	dir := filepath.Join(t.TempDir(), "newproject")
	w := NewInitWizard(dir, templates)

	// Step 1: confirm directory
	m, _ := update(t, w, keyMsg("enter"))
	iw := asInitWizard(t, m)
	if iw.step != initStepTemplate {
		t.Fatalf("expected initStepTemplate, got %d", iw.step)
	}

	// Step 2: select basic template (first, already selected)
	m, _ = update(t, m, keyMsg("enter"))
	iw = asInitWizard(t, m)
	if iw.step != initStepSetupChoice {
		t.Fatalf("expected initStepSetupChoice, got %d", iw.step)
	}

	// Step 3: navigate to "Yes" and select
	m, _ = update(t, m, keyMsg("down"))
	m, _ = update(t, m, keyMsg("enter"))
	iw = asInitWizard(t, m)
	if !iw.connActive {
		t.Fatal("connActive should be true after selecting 'Yes'")
	}
	if iw.connWizard == nil {
		t.Fatal("connWizard should not be nil")
	}

	// Step 4: now we're in the connection wizard (provider selection)
	// Select local provider
	m, _ = update(t, m, keyMsg("enter"))
	iw = asInitWizard(t, m)
	if iw.connWizard.step != stepInputHost {
		t.Fatalf("expected connection wizard at stepInputHost, got %d", iw.connWizard.step)
	}

	// Step 5: advance through host fields, type database name, submit
	m, _ = update(t, m, keyMsg("enter"))   // host
	m, _ = update(t, m, keyMsg("enter"))   // port
	m = typeString(t, m, "testdb")          // type database name
	m, _ = update(t, m, keyMsg("enter"))   // database
	m, _ = update(t, m, keyMsg("enter"))   // management db
	m, _ = update(t, m, keyMsg("enter"))   // username
	m, _ = update(t, m, keyMsg("enter")) // password → submit
	iw = asInitWizard(t, m)
	if iw.connWizard.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", iw.connWizard.step)
	}

	// Step 6: simulate successful test result
	m, _ = update(t, m, testResultMsg{success: true, info: "PostgreSQL 16.1"})
	iw = asInitWizard(t, m)
	if !iw.connWizard.testDone || !iw.connWizard.testOK {
		t.Fatal("expected test done and OK")
	}

	// Step 7: press Enter to confirm — should quit the entire combined wizard
	var cmd tea.Cmd
	m, cmd = update(t, m, keyMsg("enter"))
	iw = asInitWizard(t, m)

	if !isQuitCmd(cmd) {
		t.Fatal("expected tea.Quit after confirming connection in init wizard")
	}

	// Verify the init result has all the data
	result := iw.Result()
	if result.Cancelled {
		t.Error("should not be cancelled")
	}
	if result.Template != "basic" {
		t.Errorf("template = %q, want basic", result.Template)
	}
	if !result.SetupConfig {
		t.Error("SetupConfig should be true")
	}
	if !result.ConnResult.Tested {
		t.Error("ConnResult.Tested should be true")
	}
	if result.ConnResult.Config.Host != "localhost" {
		t.Errorf("host = %q, want localhost", result.ConnResult.Config.Host)
	}
	if result.ConnResult.Config.Port != 5432 {
		t.Errorf("port = %d, want 5432", result.ConnResult.Config.Port)
	}
}

func TestInitWizard_NoConnection_QuitsAtSetupChoice(t *testing.T) {
	templates := DefaultTemplates()
	dir := filepath.Join(t.TempDir(), "newproject")
	w := NewInitWizard(dir, templates)

	// directory → template → "No" (already selected) → enter
	m, _ := update(t, w, keyMsg("enter"))
	m, _ = update(t, m, keyMsg("enter"))
	m, cmd := update(t, m, keyMsg("enter"))
	iw := asInitWizard(t, m)

	if !isQuitCmd(cmd) {
		t.Fatal("expected tea.Quit when selecting No")
	}
	if iw.connActive {
		t.Error("connActive should be false")
	}
	result := iw.Result()
	if result.SetupConfig {
		t.Error("SetupConfig should be false")
	}
}

func TestInitWizard_ConnectionCancelledViaEsc(t *testing.T) {
	templates := DefaultTemplates()
	dir := filepath.Join(t.TempDir(), "newproject")
	w := NewInitWizard(dir, templates)

	// directory → template → "Yes" → connection wizard starts
	m, _ := update(t, w, keyMsg("enter"))
	m, _ = update(t, m, keyMsg("enter"))
	m, _ = update(t, m, keyMsg("down"))
	m, _ = update(t, m, keyMsg("enter"))
	iw := asInitWizard(t, m)
	if !iw.connActive {
		t.Fatal("should be in connection wizard")
	}

	// Esc on provider selection cancels connection wizard
	m, cmd := update(t, m, keyMsg("esc"))
	iw = asInitWizard(t, m)

	if !isQuitCmd(cmd) {
		t.Fatal("expected tea.Quit on Esc in connection wizard")
	}
	result := iw.Result()
	if !result.ConnResult.Cancelled {
		t.Error("ConnResult should be cancelled")
	}
}

// --- AWS IAM flow ---

func selectAWSIAMProvider(t *testing.T, w ConnectionWizard) tea.Model {
	t.Helper()
	// Provider list: Local(0), Azure(1), AWS(2)
	m, _ := update(t, w, keyMsg("down"))  // → Azure
	m, _ = update(t, m, keyMsg("down"))   // → AWS
	m, _ = update(t, m, keyMsg("enter"))  // Select AWS → auth selection
	wiz := asWizard(t, m)
	if wiz.step != stepSelectAuth {
		t.Fatalf("expected stepSelectAuth, got %d", wiz.step)
	}
	// First auth option is IAM
	m, _ = update(t, m, keyMsg("enter")) // Select IAM → AWS form
	return m
}

func TestConnectionWizard_AWSIAMFlow(t *testing.T) {
	mock := &mockTester{info: "AWS RDS ready"}
	w := NewConnectionWizard(WithTester(mock))

	m := selectAWSIAMProvider(t, w)
	wiz := asWizard(t, m)
	if wiz.step != stepInputAWS {
		t.Fatalf("expected stepInputAWS, got %d", wiz.step)
	}
	if len(wiz.inputs) != 5 {
		t.Fatalf("AWS form should have 5 inputs, got %d", len(wiz.inputs))
	}

	// Fill: host, port(enter=default), database, username, region
	m = typeString(t, m, "mydb.xxx.us-east-1.rds.amazonaws.com")
	m, _ = update(t, m, keyMsg("enter")) // host → port
	m, _ = update(t, m, keyMsg("enter")) // port (default 5432) → database
	m = typeString(t, m, "mydb")
	m, _ = update(t, m, keyMsg("enter")) // database → username
	m = typeString(t, m, "iam_user")
	m, _ = update(t, m, keyMsg("enter")) // username → region
	m = typeString(t, m, "us-east-1")

	var cmd tea.Cmd
	m, cmd = update(t, m, keyMsg("enter")) // region → submit
	wiz = asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	// Resolve test
	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg")
	}
	m, _ = update(t, m, result)
	m, _ = update(t, m, keyMsg("enter")) // accept → done
	wiz = asWizard(t, m)
	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
	if mock.gotCfg.AuthMethod != pgmi.AuthMethodAWSIAM {
		t.Errorf("auth = %v, want AWSIAM", mock.gotCfg.AuthMethod)
	}
	if mock.gotCfg.AWSRegion != "us-east-1" {
		t.Errorf("AWSRegion = %q, want %q", mock.gotCfg.AWSRegion, "us-east-1")
	}
}

func TestConnectionWizard_AWSIAMFlow_ValidationMissingHost(t *testing.T) {
	w := NewConnectionWizard()
	m := selectAWSIAMProvider(t, w)

	// Skip all fields without filling host
	for i := 0; i < 4; i++ {
		m, _ = update(t, m, keyMsg("enter"))
	}
	m, _ = update(t, m, keyMsg("enter")) // submit
	wiz := asWizard(t, m)
	if wiz.validationErr == "" {
		t.Error("expected validation error for empty host")
	}
}

// --- Google Cloud SQL IAM flow ---

func selectGoogleIAMProvider(t *testing.T, w ConnectionWizard) tea.Model {
	t.Helper()
	// Provider list: Local(0), Azure(1), AWS(2), Google(3)
	m, _ := update(t, w, keyMsg("down"))  // → Azure
	m, _ = update(t, m, keyMsg("down"))   // → AWS
	m, _ = update(t, m, keyMsg("down"))   // → Google
	m, _ = update(t, m, keyMsg("enter"))  // Select Google → auth selection
	wiz := asWizard(t, m)
	if wiz.step != stepSelectAuth {
		t.Fatalf("expected stepSelectAuth, got %d", wiz.step)
	}
	// First auth option is Cloud SQL IAM
	m, _ = update(t, m, keyMsg("enter"))
	return m
}

func TestConnectionWizard_GoogleIAMFlow(t *testing.T) {
	mock := &mockTester{info: "Cloud SQL ready"}
	w := NewConnectionWizard(WithTester(mock))

	m := selectGoogleIAMProvider(t, w)
	wiz := asWizard(t, m)
	if wiz.step != stepInputGoogle {
		t.Fatalf("expected stepInputGoogle, got %d", wiz.step)
	}
	if len(wiz.inputs) != 3 {
		t.Fatalf("Google form should have 3 inputs, got %d", len(wiz.inputs))
	}

	// Fill: instance, database, username
	m = typeString(t, m, "proj:region:inst")
	m, _ = update(t, m, keyMsg("enter")) // instance → database
	m = typeString(t, m, "mydb")
	m, _ = update(t, m, keyMsg("enter")) // database → username
	m = typeString(t, m, "iam_user@proj.iam")

	var cmd tea.Cmd
	m, cmd = update(t, m, keyMsg("enter")) // username → submit
	wiz = asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg")
	}
	m, _ = update(t, m, result)
	m, _ = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
	if mock.gotCfg.AuthMethod != pgmi.AuthMethodGoogleIAM {
		t.Errorf("auth = %v, want GoogleIAM", mock.gotCfg.AuthMethod)
	}
	if mock.gotCfg.GoogleInstance != "proj:region:inst" {
		t.Errorf("instance = %q, want %q", mock.gotCfg.GoogleInstance, "proj:region:inst")
	}
}

func TestConnectionWizard_GoogleIAMFlow_ValidationMissingInstance(t *testing.T) {
	w := NewConnectionWizard()
	m := selectGoogleIAMProvider(t, w)

	// Skip instance, type database, skip username → submit
	m, _ = update(t, m, keyMsg("enter")) // instance (empty) → database
	m = typeString(t, m, "mydb")
	m, _ = update(t, m, keyMsg("enter")) // database → username
	m, _ = update(t, m, keyMsg("enter")) // username → submit
	wiz := asWizard(t, m)
	if wiz.validationErr == "" {
		t.Error("expected validation error for empty instance")
	}
}

// --- Connection String flow ---

func selectConnStringProvider(t *testing.T, w ConnectionWizard) tea.Model {
	t.Helper()
	// Provider list: Local(0), Azure(1), AWS(2), Google(3), Custom(4)
	m, _ := update(t, w, keyMsg("down"))  // → Azure
	m, _ = update(t, m, keyMsg("down"))   // → AWS
	m, _ = update(t, m, keyMsg("down"))   // → Google
	m, _ = update(t, m, keyMsg("down"))   // → Custom
	m, _ = update(t, m, keyMsg("enter"))  // Select Custom → only 1 auth → skip to form
	return m
}

func TestConnectionWizard_ConnStringFlow(t *testing.T) {
	mock := &mockTester{info: "Connected"}
	w := NewConnectionWizard(WithTester(mock))

	m := selectConnStringProvider(t, w)
	wiz := asWizard(t, m)
	if wiz.step != stepInputConnString {
		t.Fatalf("expected stepInputConnString, got %d", wiz.step)
	}
	if len(wiz.inputs) != 1 {
		t.Fatalf("ConnString form should have 1 input, got %d", len(wiz.inputs))
	}

	m = typeString(t, m, "postgresql://user:pass@localhost:5432/mydb")
	var cmd tea.Cmd
	m, cmd = update(t, m, keyMsg("enter")) // submit
	wiz = asWizard(t, m)
	if wiz.step != stepTestConnection {
		t.Fatalf("expected stepTestConnection, got %d", wiz.step)
	}

	msgs := drainCmds(cmd)
	result, ok := findTestResult(msgs)
	if !ok {
		t.Fatal("expected testResultMsg")
	}
	m, _ = update(t, m, result)
	m, _ = update(t, m, keyMsg("enter"))
	wiz = asWizard(t, m)
	if wiz.step != stepDone {
		t.Errorf("step = %d, want stepDone", wiz.step)
	}
}

func TestConnectionWizard_ConnStringFlow_ValidationMissing(t *testing.T) {
	w := NewConnectionWizard()
	m := selectConnStringProvider(t, w)

	// Submit empty connection string
	m, _ = update(t, m, keyMsg("enter"))
	wiz := asWizard(t, m)
	if wiz.validationErr == "" {
		t.Error("expected validation error for empty connection string")
	}
}

func TestConnectionWizard_CtrlC_Cancels(t *testing.T) {
	w := NewConnectionWizard()
	_, cmd := update(t, w, tea.KeyMsg{Type: tea.KeyCtrlC})

	if !isQuitCmd(cmd) {
		t.Error("ctrl+c should produce tea.Quit")
	}
}

// --- View tests ---

func TestConnectionWizard_View_ProviderStep(t *testing.T) {
	w := NewConnectionWizard()
	view := w.View()

	if !strings.Contains(view, "Connection Setup") {
		t.Error("View at provider step should contain 'Connection Setup'")
	}
	for _, p := range providers {
		if !strings.Contains(view, p.Name) {
			t.Errorf("View at provider step should contain provider name %q", p.Name)
		}
	}
}

func TestConnectionWizard_View_InputFormStep(t *testing.T) {
	w := NewConnectionWizard()
	m, _ := update(t, w, keyMsg("enter")) // select local → host form

	view := m.View()
	for _, label := range []string{"Host:", "Port:", "Database:"} {
		if !strings.Contains(view, label) {
			t.Errorf("View at input form should contain %q", label)
		}
	}
}

func TestConnectionWizard_View_TestConnectionStep(t *testing.T) {
	w := NewConnectionWizard()
	m, _ := selectLocalAndFillDB(t, w)

	// Simulate success
	m, _ = update(t, m, testResultMsg{success: true, info: "PostgreSQL 16.1"})
	view := m.View()
	if !strings.Contains(view, "Connected successfully") {
		t.Error("View after success should contain 'Connected successfully'")
	}

	// Simulate failure path
	w2 := NewConnectionWizard()
	m2, _ := selectLocalAndFillDB(t, w2)
	m2, _ = update(t, m2, testResultMsg{success: false, err: fmt.Errorf("refused")})
	view2 := m2.View()
	if !strings.Contains(view2, "Connection failed") {
		t.Error("View after failure should contain 'Connection failed'")
	}
}

func TestConnectionWizard_TabNavigation(t *testing.T) {
	w := NewConnectionWizard()
	m, _ := update(t, w, keyMsg("enter")) // local → host form
	wiz := asWizard(t, m)
	if wiz.focusIndex != 0 {
		t.Fatalf("initial focus = %d, want 0", wiz.focusIndex)
	}

	// Tab → focus 1
	m, _ = update(t, m, keyMsg("tab"))
	wiz = asWizard(t, m)
	if wiz.focusIndex != 1 {
		t.Errorf("after tab, focusIndex = %d, want 1", wiz.focusIndex)
	}

	// Shift+tab → focus 0
	m, _ = update(t, m, keyMsg("shift+tab"))
	wiz = asWizard(t, m)
	if wiz.focusIndex != 0 {
		t.Errorf("after shift+tab, focusIndex = %d, want 0", wiz.focusIndex)
	}
}

func TestConnectionWizard_TabAtBoundary(t *testing.T) {
	w := NewConnectionWizard()
	m, _ := update(t, w, keyMsg("enter")) // local → host form (6 inputs)

	// Shift+tab at first field stays at 0
	m, _ = update(t, m, keyMsg("shift+tab"))
	wiz := asWizard(t, m)
	if wiz.focusIndex != 0 {
		t.Errorf("shift+tab at first field: focusIndex = %d, want 0", wiz.focusIndex)
	}

	// Tab to last field
	for i := 0; i < 5; i++ {
		m, _ = update(t, m, keyMsg("tab"))
	}
	wiz = asWizard(t, m)
	if wiz.focusIndex != 5 {
		t.Fatalf("after 5 tabs, focusIndex = %d, want 5", wiz.focusIndex)
	}

	// Tab at last field stays put
	m, _ = update(t, m, keyMsg("tab"))
	wiz = asWizard(t, m)
	if wiz.focusIndex != 5 {
		t.Errorf("tab at last field: focusIndex = %d, want 5", wiz.focusIndex)
	}
}

func TestConnectionWizard_BackFromAuthToProvider(t *testing.T) {
	w := NewConnectionWizard()
	// Navigate to Azure (has multiple auth methods)
	m, _ := update(t, w, keyMsg("down"))
	m, _ = update(t, m, keyMsg("enter"))
	wiz := asWizard(t, m)
	if wiz.step != stepSelectAuth {
		t.Fatalf("expected stepSelectAuth, got %d", wiz.step)
	}

	// Esc at auth → back to provider
	m, _ = update(t, m, keyMsg("esc"))
	wiz = asWizard(t, m)
	if wiz.step != stepSelectProvider {
		t.Errorf("after esc at auth, step = %d, want stepSelectProvider", wiz.step)
	}
}

func TestConnectionWizard_ProviderBounds(t *testing.T) {
	w := NewConnectionWizard()

	// Up at 0 stays 0
	m, _ := update(t, w, keyMsg("up"))
	wiz := asWizard(t, m)
	if wiz.providerIdx != 0 {
		t.Errorf("up at 0: providerIdx = %d, want 0", wiz.providerIdx)
	}

	// Navigate to max
	maxIdx := len(providers) - 1
	for i := 0; i < maxIdx+5; i++ {
		m, _ = update(t, m, keyMsg("down"))
	}
	wiz = asWizard(t, m)
	if wiz.providerIdx != maxIdx {
		t.Errorf("down past max: providerIdx = %d, want %d", wiz.providerIdx, maxIdx)
	}
}

func TestConnectionWizard_InvalidPortDefaultsTo5432(t *testing.T) {
	mock := &mockTester{info: "PostgreSQL 16.1"}
	w := NewConnectionWizard(WithTester(mock))

	// Select local → host form
	m, _ := update(t, w, keyMsg("enter"))

	// Clear host default, type host
	m, _ = update(t, m, keyMsg("enter")) // host → port

	// Clear port default and type invalid
	wiz := asWizard(t, m)
	wiz.inputs[1].SetValue("abc")
	m = wiz

	m, _ = update(t, m, keyMsg("enter"))           // port → database
	m = typeString(t, m, "testdb")
	m, _ = update(t, m, keyMsg("enter"))            // database → mgmt db
	m, _ = update(t, m, keyMsg("enter"))            // mgmt db → username
	m, _ = update(t, m, keyMsg("enter"))            // username → password
	m, _ = update(t, m, keyMsg("enter"))            // password → submit

	wiz = asWizard(t, m)
	if wiz.result.Config.Port != 5432 {
		t.Errorf("invalid port should default to 5432, got %d", wiz.result.Config.Port)
	}
}

func TestConnectionWizard_AzureValidation_MissingDatabase(t *testing.T) {
	w := NewConnectionWizard()

	// Navigate to Azure → Entra ID
	m, _ := update(t, w, keyMsg("down"))
	m, _ = update(t, m, keyMsg("enter"))  // Azure → auth
	m, _ = update(t, m, keyMsg("enter"))  // Entra ID → Azure form

	// Type server name
	m = typeString(t, m, "myserver.postgres.database.azure.com")
	m, _ = update(t, m, keyMsg("enter"))  // server → database
	// Skip database (empty)
	m, _ = update(t, m, keyMsg("enter"))  // database → username
	m, _ = update(t, m, keyMsg("enter"))  // username → submit
	wiz := asWizard(t, m)
	if wiz.validationErr == "" {
		t.Error("expected validation error for empty Azure database")
	}
	if !strings.Contains(wiz.validationErr, "database") {
		t.Errorf("validation error should mention database, got: %q", wiz.validationErr)
	}
}

// --- InitWizard View tests ---

func TestInitWizard_View_DirectoryStep(t *testing.T) {
	templates := DefaultTemplates()
	dir := filepath.Join(t.TempDir(), "newproject")
	w := NewInitWizard(dir, templates)

	view := w.View()
	if !strings.Contains(view, "pgmi init") {
		t.Error("View at directory step should contain 'pgmi init'")
	}
	if !strings.Contains(view, "Directory:") {
		t.Error("View at directory step should contain 'Directory:'")
	}
}

func TestInitWizard_View_TemplateStep(t *testing.T) {
	templates := DefaultTemplates()
	dir := filepath.Join(t.TempDir(), "newproject")
	w := NewInitWizard(dir, templates)

	// Confirm directory → template step
	m, _ := update(t, w, keyMsg("enter"))
	view := m.View()
	if !strings.Contains(view, "basic") {
		t.Error("View at template step should contain 'basic'")
	}
	if !strings.Contains(view, "advanced") {
		t.Error("View at template step should contain 'advanced'")
	}
}

func TestInitWizard_CheckDirBlocking(t *testing.T) {
	// Empty dir → no blocking
	emptyDir := filepath.Join(t.TempDir(), "empty")
	os.MkdirAll(emptyDir, 0o755)
	if blocking := checkDirBlocking(emptyDir); len(blocking) != 0 {
		t.Errorf("empty dir should not block, got %v", blocking)
	}

	// pgmi.yaml and .env → allowed
	managedDir := filepath.Join(t.TempDir(), "managed")
	os.MkdirAll(managedDir, 0o755)
	os.WriteFile(filepath.Join(managedDir, "pgmi.yaml"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(managedDir, ".env"), []byte(""), 0o644)
	if blocking := checkDirBlocking(managedDir); len(blocking) != 0 {
		t.Errorf("pgmi-managed files should not block, got %v", blocking)
	}

	// Other files → blocked
	blockedDir := filepath.Join(t.TempDir(), "blocked")
	os.MkdirAll(blockedDir, 0o755)
	os.WriteFile(filepath.Join(blockedDir, "main.go"), []byte("package main"), 0o644)
	blocking := checkDirBlocking(blockedDir)
	if len(blocking) == 0 {
		t.Error("non-pgmi files should block")
	}

	// Non-existent dir → no blocking
	if blocking := checkDirBlocking(filepath.Join(t.TempDir(), "nonexistent")); len(blocking) != 0 {
		t.Errorf("non-existent dir should not block, got %v", blocking)
	}
}
