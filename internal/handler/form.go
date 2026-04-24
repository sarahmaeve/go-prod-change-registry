package handler

import (
	"errors"
	"net/http"
)

// maxFormBytes bounds POST form bodies handled by this package. The
// dashboard and /login endpoints accept only short fields (auth tokens,
// CSRF tokens), so 8 KiB is generous. The bound exists to prevent
// unbounded memory consumption from a crafted request body — see gosec
// G120 (CWE-409).
const maxFormBytes = 8 << 10

// parseBoundedPostForm wraps r.Body in http.MaxBytesReader and calls
// ParseForm. It writes 413 on a body-too-large error and 400 on any other
// parse failure, then returns false so the caller can return immediately.
// On success the caller may read values via r.PostFormValue / r.FormValue.
func parseBoundedPostForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return false
	}
	return true
}
