package metadata

import (
	"crypto/md5"

	"github.com/google/uuid"
)

// GenerateFallbackID creates a deterministic UUID from the raw file path using
// MD5, matching the PostgreSQL view's md5(s.path::bytea)::uuid algorithm.
// The path must include the "./" prefix the scanner adds.
//
// The equality with pgmi_plan_view.generic_id is load-bearing — users key
// idempotency tracking off the view while `pgmi metadata plan` reports this —
// and it is checked live, on both UTF8 and LATIN1, by
// TestPlanViewGenericIDMatchesGoFallbackAcrossEncodings.
func GenerateFallbackID(path string) uuid.UUID {
	h := md5.Sum([]byte(path))
	return uuid.UUID(h)
}
