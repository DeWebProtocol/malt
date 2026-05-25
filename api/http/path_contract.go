// Locked path stat/content HTTP semantics.
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
