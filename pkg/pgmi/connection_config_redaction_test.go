package pgmi

import (
	"fmt"
	"strings"
	"testing"
)

// ConnectionConfig carries three secrets. Anything that formats one — a debug
// line, a wrapped error, a panic dump — must not print them, and redactPasswords
// cannot be the safety net: it matches `password=` or a URI, while a struct
// renders `Password:hunter2` under %+v and a bare positional value under %v.
func TestConnectionConfig_FormattingNeverPrintsSecrets(t *testing.T) {
	cfg := ConnectionConfig{
		Host:              "db.internal",
		Port:              5432,
		Database:          "app",
		Username:          "deployer",
		SSLMode:           "require",
		Password:          "pw-must-not-appear",
		AzureClientSecret: "azure-must-not-appear",
		SSLPassword:       "sslpw-must-not-appear",
	}
	secrets := []string{cfg.Password, cfg.AzureClientSecret, cfg.SSLPassword}

	// Passed as `any`, the way fmt.Errorf and every logger receive it — which is
	// also how a leak would actually reach output. %v, %s and %q consult
	// Stringer; %#v consults GoStringer. The pointer is covered too because both
	// methods take a value receiver.
	var val, ptr any = cfg, &cfg

	renders := map[string]string{
		"%v value":    fmt.Sprintf("%v", val),
		"%+v value":   fmt.Sprintf("%+v", val),
		"%s value":    fmt.Sprintf("%s", val),
		"%q value":    fmt.Sprintf("%q", val),
		"%#v value":   fmt.Sprintf("%#v", val),
		"%v ptr":      fmt.Sprintf("%v", ptr),
		"%+v ptr":     fmt.Sprintf("%+v", ptr),
		"%#v ptr":     fmt.Sprintf("%#v", ptr),
		"wrapped":     fmt.Errorf("connect failed: %v", val).Error(),
		"wrapped ptr": fmt.Errorf("connect failed: %+v", ptr).Error(),
	}

	for verb, out := range renders {
		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("%s leaked %q: %s", verb, secret, out)
			}
		}
		// A render that dropped everything would pass the check above for the
		// wrong reason.
		if !strings.Contains(out, "db.internal") || !strings.Contains(out, "deployer") {
			t.Errorf("%s lost the non-secret fields that make it useful: %s", verb, out)
		}
	}
}

// An unset secret must read as unset rather than as one that is present, so the
// rendering stays useful for diagnosing "no password was supplied".
func TestConnectionConfig_StringDistinguishesUnsetSecrets(t *testing.T) {
	set := ConnectionConfig{Host: "h", Password: "x"}.String()
	if !strings.Contains(set, "Password:[redacted]") {
		t.Errorf("a set password should read as redacted: %s", set)
	}

	unset := ConnectionConfig{Host: "h"}.String()
	if !strings.Contains(unset, "Password:unset") {
		t.Errorf("an absent password should read as unset: %s", unset)
	}
}
