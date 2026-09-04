package pcrclient //nolint:testpackage // fuzzing the unexported response validator is intentional

import (
	"bytes"
	"strings"
	"testing"
	"unicode"
)

func FuzzDecodeErrorResponse(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("{\"error\":{\"code\":\"validation_error\",\"message\":\"phase must be exactly start or end\"}}"),
		[]byte("{\"error\":{\"code\":\"internal-error\",\"message\":\"invalid code\"}}"),
		[]byte("{\"error\":{\"code\":\"validation_error\",\"message\":\"line one\\nline two\"}}"),
		[]byte("{\"error\":{\"code\":\"validation_error\",\"message\":\"\"}}"),
		[]byte("not JSON"),
		{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxErrorResponseSize+1 {
			data = data[:maxErrorResponseSize+1]
		}

		code, message := decodeErrorResponse(bytes.NewReader(data))
		repeatedCode, repeatedMessage := decodeErrorResponse(bytes.NewReader(data))
		if code != repeatedCode || message != repeatedMessage {
			t.Fatalf("decodeErrorResponse() = (%q, %q), repeated call = (%q, %q)", code, message, repeatedCode, repeatedMessage)
		}
		if (code == "") != (message == "") {
			t.Fatalf("decodeErrorResponse() returned an incomplete detail pair: code=%q message=%q", code, message)
		}
		if code == "" {
			return
		}
		if !safeErrorCode(code) || !safeErrorMessage(message) {
			t.Fatalf("decodeErrorResponse() returned unsafe details: code=%q message=%q", code, message)
		}
		if code != strings.TrimSpace(code) || message != strings.TrimSpace(message) {
			t.Fatalf("decodeErrorResponse() returned untrimmed details: code=%q message=%q", code, message)
		}
		diagnostic := statusError("request", "pcr.example.com", 400, code, message).Error()
		if strings.IndexFunc(diagnostic, unsafeDiagnosticRune) >= 0 {
			t.Fatalf("statusError() returned an unsafe diagnostic: %q", diagnostic)
		}
	})
}

func unsafeDiagnosticRune(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}
