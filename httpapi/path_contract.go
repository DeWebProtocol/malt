// Locked path stat/content HTTP semantics and `malt get` output rules.
package httpapi

import "net/http"

// HTTP status codes for current-root file endpoints.
// Use the standard library constants when implementing handlers.
const (
	// StatusPathNotFound: GET stat/content when the resolved object path does not exist.
	StatusPathNotFound = http.StatusNotFound // 404

	// StatusContentIsDirectory: GET content when the target is a directory, not a file.
	StatusContentIsDirectory = http.StatusConflict // 409

	// StatusRangeNotSatisfiable: Range header cannot be satisfied for the file.
	StatusRangeNotSatisfiable = http.StatusRequestedRangeNotSatisfiable // 416
)

// GetDefaultOutputRules documents locked `malt get` local output behavior (no runtime here).
//
//  1. If local-output is omitted and the target path is non-root, write to ./<basename>
//     (basename of the MALT path).
//  2. If the target is the current root, local-output is required.
//  3. If the target is a directory and local-output is omitted, materialize under ./<basename>.
const GetDefaultOutputRules = "see plan.md: non-root default ./<basename>, root requires output, directory restores to ./<basename>"
