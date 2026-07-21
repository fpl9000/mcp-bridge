// errors.go implements the error response convention of design spec Section 3.19:
// every memory tool failure carries both a stable machine-readable code and a
// natural-language message the LLM can act on directly, without ever leaking
// bridge-internal details (file paths, the mutex, frontmatter) into the message.
package main

import "encoding/json"

// Stable error codes reachable by this build. The full design defines
// MAINTENANCE_IN_PROGRESS as well, but that belongs to the deferred
// maintenance feature and is intentionally omitted here.
const (
	ErrCodeInvalidHandle    = "INVALID_HANDLE"
	ErrCodeMalformedHandle  = "MALFORMED_HANDLE"
	ErrCodeBlockNotFound    = "BLOCK_NOT_FOUND"
	ErrCodeInvalidBlockName = "INVALID_BLOCK_NAME"
	ErrCodeSummaryRequired  = "SUMMARY_REQUIRED"
	ErrCodeSummaryTooLong   = "SUMMARY_TOO_LONG"
	ErrCodeInternalError    = "INTERNAL_ERROR"
)

// ErrorDetail is the "error" object nested inside every failure response.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Context map[string]any `json:"context,omitempty"`
}

// ErrorResponse is the full JSON shape returned to the LLM for any failed
// memory tool call. Handle is omitted (via omitempty) when the error itself
// concerns an unrecognized or malformed handle, per Section 3.19's schema note.
type ErrorResponse struct {
	Handle string      `json:"handle,omitempty"`
	Ok     bool        `json:"ok"`
	Error  ErrorDetail `json:"error"`
}

// newErrorResponse builds the standard error envelope and marshals it to the
// JSON text that becomes the tool result. Marshaling a fixed, well-formed
// struct cannot fail, so the error return from json.Marshal is deliberately
// ignored here.
func newErrorResponse(handle string, code string, message string, context map[string]any) string {
	response := ErrorResponse{
		Handle: handle,
		Ok:     false,
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Context: context,
		},
	}

	encoded, _ := json.Marshal(response)
	return string(encoded)
}

// internalErrorMessage is the generic, abstraction-safe text returned to the
// LLM for INTERNAL_ERROR responses. The technical detail that would otherwise
// leak implementation internals goes to the server-side log instead (Section
// 3.19: "the message stays generic... the technical detail goes to the
// server-side log file").
const internalErrorMessage = "An internal bridge error occurred; the operation was not completed. Please report this to the user."
