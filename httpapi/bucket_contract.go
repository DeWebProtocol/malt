// Locked bucket stat/content HTTP semantics and `malt get` output rules (Implementation plan Phase 0).
package httpapi

import "net/http"

// HTTP status codes for bucket file endpoints (locked in plan.md).
// Use the standard library constants when implementing handlers.
const (
	// StatusBucketPathNotFound: GET stat/content when the resolved object path does not exist.
	StatusBucketPathNotFound = http.StatusNotFound // 404

	// StatusBucketContentIsDirectory: GET content when the target is a directory, not a file.
	StatusBucketContentIsDirectory = http.StatusConflict // 409

	// StatusBucketRangeNotSatisfiable: Range header cannot be satisfied for the file.
	StatusBucketRangeNotSatisfiable = http.StatusRequestedRangeNotSatisfiable // 416
)

// GetDefaultOutputRules documents locked `malt get` local output behavior (no runtime here).
//
//  1. If local-output is omitted and the target path is non-root, write to ./<basename>
//     (basename of the MALT path).
//  2. If the target is the bucket root, local-output is required.
//  3. If the target is a directory and local-output is omitted, materialize under ./<basename>.
const GetDefaultOutputRules = "see plan.md: non-root default ./<basename>, root requires output, directory restores to ./<basename>"
