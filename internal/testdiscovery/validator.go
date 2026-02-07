package testdiscovery

import (
	"regexp"
	"strings"
)

var (
	savepointRe = regexp.MustCompile(`^SAVEPOINT\s+(__pgmi_\d+__)\s*;?$`)
	rollbackRe  = regexp.MustCompile(`^ROLLBACK\s+TO\s+SAVEPOINT\s+(__pgmi_\d+__)\s*;?$`)
	releaseRe   = regexp.MustCompile(`^RELEASE\s+SAVEPOINT\s+(__pgmi_\d+__)\s*;?$`)
)

type SavepointOperation string

const (
	OpSavepoint SavepointOperation = "SAVEPOINT"
	OpRollback  SavepointOperation = "ROLLBACK"
	OpRelease   SavepointOperation = "RELEASE"
)

type SavepointState struct {
	Name       string
	CreatedAt  int
	RolledBack bool
	Released   bool
}

type ValidationError struct {
	Invariant string
	Message   string
	Ordinal   int
	Savepoint string
}

type ValidationResult struct {
	Valid  bool
	Errors []ValidationError
}

type SavepointValidator struct {
	savepoints map[string]*SavepointState
	stack      []string
}

func NewSavepointValidator() *SavepointValidator {
	return &SavepointValidator{
		savepoints: make(map[string]*SavepointState),
		stack:      make([]string, 0),
	}
}

func (v *SavepointValidator) Validate(rows []TestScriptRow) ValidationResult {
	result := ValidationResult{Valid: true, Errors: []ValidationError{}}

	if len(rows) == 0 {
		return result
	}

	v.savepoints = make(map[string]*SavepointState)
	v.stack = make([]string, 0)

	v.validateMonotonicOrdinals(rows, &result)
	v.validateDirectoryStructure(rows, &result)
	v.validateSavepointLifecycle(rows, &result)
	v.validateNestingOrder(rows, &result)

	return result
}

func (v *SavepointValidator) validateMonotonicOrdinals(rows []TestScriptRow, result *ValidationResult) {
	for i := 1; i < len(rows); i++ {
		if rows[i].Ordinal <= rows[i-1].Ordinal {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "MonotonicOrdinals",
				Message:   "ordinal must strictly increase",
				Ordinal:   rows[i].Ordinal,
			})
		}
	}
}

func (v *SavepointValidator) validateDirectoryStructure(rows []TestScriptRow, result *ValidationResult) {
	dirFixtures := make(map[string]int)   // directory -> first fixture ordinal
	dirFirstTest := make(map[string]int)  // directory -> first test ordinal
	dirTeardowns := make(map[string]int)  // directory -> count of teardowns
	dirPresent := make(map[string]bool)   // all directories seen

	for _, row := range rows {
		dirPresent[row.Directory] = true

		switch row.StepType {
		case "fixture":
			if _, exists := dirFixtures[row.Directory]; !exists {
				dirFixtures[row.Directory] = row.Ordinal
			}
		case "test":
			if _, exists := dirFirstTest[row.Directory]; !exists {
				dirFirstTest[row.Directory] = row.Ordinal
			}
		case "teardown":
			dirTeardowns[row.Directory]++
		}
	}

	for dir := range dirPresent {
		if count := dirTeardowns[dir]; count != 1 {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "DirectoryStructure",
				Message:   "each directory must have exactly one teardown",
				Ordinal:   0,
				Savepoint: dir,
			})
		}
	}

	for dir, fixtureOrd := range dirFixtures {
		if testOrd, hasTest := dirFirstTest[dir]; hasTest && fixtureOrd > testOrd {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "DirectoryStructure",
				Message:   "fixture must come before tests in same directory",
				Ordinal:   fixtureOrd,
				Savepoint: dir,
			})
		}
	}
}

func (v *SavepointValidator) validateSavepointLifecycle(rows []TestScriptRow, result *ValidationResult) {
	v.savepoints = make(map[string]*SavepointState)
	v.stack = make([]string, 0)

	for _, row := range rows {
		if row.PreExec != nil {
			v.processLifecycle(*row.PreExec, row.Ordinal)
		}
		if row.PostExec != nil {
			v.processLifecycle(*row.PostExec, row.Ordinal)
		}
	}

	for name, state := range v.savepoints {
		if !state.RolledBack {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "SavepointPairing",
				Message:   "savepoint missing ROLLBACK TO",
				Ordinal:   state.CreatedAt,
				Savepoint: name,
			})
		}
		if !state.Released {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "SavepointPairing",
				Message:   "savepoint missing RELEASE",
				Ordinal:   state.CreatedAt,
				Savepoint: name,
			})
		}
	}
}

func (v *SavepointValidator) processLifecycle(sql string, ordinal int) {
	name, op, ok := parseSavepoint(sql)
	if !ok {
		return
	}

	switch op {
	case OpSavepoint:
		v.savepoints[name] = &SavepointState{
			Name:      name,
			CreatedAt: ordinal,
		}
		v.stack = append(v.stack, name)

	case OpRollback:
		if state, exists := v.savepoints[name]; exists {
			state.RolledBack = true
			// ROLLBACK TO destroys all savepoints created after this one
			// Find position of this savepoint in stack and mark all after as implicitly released
			stackIdx := -1
			for i, sp := range v.stack {
				if sp == name {
					stackIdx = i
					break
				}
			}
			if stackIdx >= 0 {
				for i := stackIdx + 1; i < len(v.stack); i++ {
					if laterState, exists := v.savepoints[v.stack[i]]; exists {
						laterState.RolledBack = true
						laterState.Released = true
					}
				}
				v.stack = v.stack[:stackIdx+1]
			}
		}

	case OpRelease:
		if state, exists := v.savepoints[name]; exists {
			state.Released = true
			// Remove from stack
			for i := len(v.stack) - 1; i >= 0; i-- {
				if v.stack[i] == name {
					v.stack = append(v.stack[:i], v.stack[i+1:]...)
					break
				}
			}
		}
	}
}

func (v *SavepointValidator) validateNestingOrder(rows []TestScriptRow, result *ValidationResult) {
	v.savepoints = make(map[string]*SavepointState)
	v.stack = make([]string, 0)

	for _, row := range rows {
		if row.PreExec != nil {
			v.trackNesting(*row.PreExec, row.Ordinal, result)
		}
		if row.PostExec != nil {
			v.trackNesting(*row.PostExec, row.Ordinal, result)
		}
	}

	if len(v.stack) > 0 {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Invariant: "NoOrphans",
			Message:   "savepoint stack not empty at end",
			Savepoint: strings.Join(v.stack, ", "),
		})
	}
}

func (v *SavepointValidator) trackNesting(sql string, ordinal int, result *ValidationResult) {
	name, op, ok := parseSavepoint(sql)
	if !ok {
		return
	}

	switch op {
	case OpSavepoint:
		v.stack = append(v.stack, name)

	case OpRollback:
		// ROLLBACK TO destroys all savepoints created after the target
		stackIdx := -1
		for i, sp := range v.stack {
			if sp == name {
				stackIdx = i
				break
			}
		}
		if stackIdx >= 0 {
			v.stack = v.stack[:stackIdx+1]
		}

	case OpRelease:
		if len(v.stack) == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "NestingOrder",
				Message:   "RELEASE with empty stack",
				Ordinal:   ordinal,
				Savepoint: name,
			})
			return
		}
		top := v.stack[len(v.stack)-1]
		if top != name {
			result.Valid = false
			result.Errors = append(result.Errors, ValidationError{
				Invariant: "NestingOrder",
				Message:   "RELEASE out of order: expected " + top + ", got " + name,
				Ordinal:   ordinal,
				Savepoint: name,
			})
		}
		v.stack = v.stack[:len(v.stack)-1]
	}
}

func parseSavepoint(sql string) (name string, op SavepointOperation, ok bool) {
	sql = strings.TrimSpace(sql)

	if m := savepointRe.FindStringSubmatch(sql); m != nil {
		return m[1], OpSavepoint, true
	}
	if m := rollbackRe.FindStringSubmatch(sql); m != nil {
		return m[1], OpRollback, true
	}
	if m := releaseRe.FindStringSubmatch(sql); m != nil {
		return m[1], OpRelease, true
	}

	return "", "", false
}
