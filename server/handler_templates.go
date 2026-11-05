package server

import (
	"encoding/json"
	"net/http"
)

// Handler templates shared by the HTTP layer.
//
// These helpers exist only to remove copy-pasted boilerplate. Each one performs
// exactly the operations the inline code it replaced performed, including the
// error status and message text: callers pass the same code and message they used
// before, so the wire contract is byte-for-byte unchanged.
//
// Deliberately NOT unified here:
//   - s.pg(w) is already the single "database unavailable" guard (one
//     implementation, used by every handler that needs the store);
//   - writeErr/writeJSON are already the single error/success writers;
//   - decoders that need special handling (MaxBytesError, DisallowUnknownFields,
//     streaming/NDJSON, logging, custom statuses) stay inline — see
//     scripts/server-refactor/REPORT.md for the exception list.

// writeOK is the "200 + JSON body" success path. It is exactly writeJSON(w, 200, v).
func writeOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

// decodeBodyErr decodes one JSON value from r.Body. On failure it writes
// code + err.Error(), matching the inline `json.NewDecoder(r.Body).Decode`
// blocks it replaces.
func decodeBodyErr(w http.ResponseWriter, r *http.Request, dst any, code int) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, code, err.Error())
		return false
	}
	return true
}

// decodeBodyMsg is decodeBodyErr with a fixed message.
func decodeBodyMsg(w http.ResponseWriter, r *http.Request, dst any, code int, msg string) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, code, msg)
		return false
	}
	return true
}

// decodeBodyBadJSON is decodeBodyErr with the historical "bad json: " prefix.
func decodeBodyBadJSON(w http.ResponseWriter, r *http.Request, dst any, code int) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, code, "bad json: "+err.Error())
		return false
	}
	return true
}

// decodeBytesErr is the []byte variant of decodeBodyErr. It keeps json.Unmarshal
// (not a Decoder) because the inline code it replaces used Unmarshal semantics.
func decodeBytesErr(w http.ResponseWriter, data []byte, dst any, code int) bool {
	if err := json.Unmarshal(data, dst); err != nil {
		writeErr(w, code, err.Error())
		return false
	}
	return true
}

// decodeBytesMsg is decodeBytesErr with a fixed message.
func decodeBytesMsg(w http.ResponseWriter, data []byte, dst any, code int, msg string) bool {
	if err := json.Unmarshal(data, dst); err != nil {
		writeErr(w, code, msg)
		return false
	}
	return true
}

// decodeBytesBadJSON is decodeBytesErr with the historical "bad json: " prefix.
func decodeBytesBadJSON(w http.ResponseWriter, data []byte, dst any, code int) bool {
	if err := json.Unmarshal(data, dst); err != nil {
		writeErr(w, code, "bad json: "+err.Error())
		return false
	}
	return true
}
