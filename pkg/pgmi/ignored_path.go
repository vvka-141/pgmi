package pgmi

// IgnoredPath is a project path that discovery did not load, and why.
type IgnoredPath struct {
	// Path in ./ form; a directory ends with /.
	Path string
	// Rule is IgnoreRuleBuiltIn or IgnoreRulePgmiignore.
	Rule string
}

const (
	IgnoreRuleBuiltIn    = "built-in"
	IgnoreRulePgmiignore = ".pgmiignore"
)
