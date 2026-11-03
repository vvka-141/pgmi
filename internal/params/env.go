package params

import (
	"fmt"
	"strings"
)

// EnvPrefix marks the environment variables that become deploy parameters.
// Only these are read: the rest of the environment (CI tokens, cloud
// credentials) never reaches the session.
const EnvPrefix = "PGMI_PARAM_"

// FromEnviron returns the parameters named by PGMI_PARAM_<NAME> entries of
// environ (os.Environ format), keyed by <NAME>. An empty value is an error:
// CI systems expand a missing secret to "", and an empty string passes the
// IS NULL check in deploy.sql that a missing parameter fails.
func FromEnviron(environ []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		key, ok := strings.CutPrefix(name, EnvPrefix)
		if !ok {
			continue
		}
		if key == "" {
			return nil, fmt.Errorf("environment variable %s has no parameter name after the prefix", name)
		}
		if value == "" {
			return nil, fmt.Errorf("environment variable %s is set but empty; unset it, or check that the secret it is mapped from exists", name)
		}
		result[key] = value
	}
	return result, nil
}
