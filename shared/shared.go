// Package shared holds the files every pgmi binary embeds: the scaffold
// templates and skills, the session SQL and contract, the AI overview and the
// terminal art. The Rust binary reads the same files from this folder.
package shared

import "embed"

//go:embed all:templates
var Templates embed.FS

//go:embed all:skills
var Skills embed.FS

//go:embed all:ai
var AI embed.FS

//go:embed session/schema.sql
var SessionSchema string

//go:embed contract/api-v1.sql
var ContractV1 string

var (
	//go:embed manager/settings.sql
	ManagerSettingsSQL string
	//go:embed manager/role_settings.sql
	ManagerRoleSettingsSQL string
	//go:embed manager/acl.sql
	ManagerACLSQL string
	//go:embed manager/deploy_locked.sql
	ManagerDeployLockedSQL string
	//go:embed manager/restore_gaps.sql
	ManagerRestoreGapsSQL string
)

//go:embed art/logo.txt
var Logo string

//go:embed art/skull.txt
var Skull string
