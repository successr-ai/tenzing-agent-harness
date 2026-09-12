package ui

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestHandlerDebugFlag proves the index page carries the server's --debug
// state as the DEBUG constant the UI gates verbose thinking and tool output
// on, and that no placeholder survives.
func TestHandlerDebugFlag(t *testing.T) {
	for _, debug := range []bool{false, true} {
		rec := httptest.NewRecorder()
		Handler(debug).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("debug=%v: content-type = %q", debug, ct)
		}
		body := rec.Body.String()
		want := "const DEBUG = " + strconv.FormatBool(debug) + ";"
		if !strings.Contains(body, want) {
			t.Errorf("debug=%v: index missing %q", debug, want)
		}
		if strings.Contains(body, debugPlaceholder) {
			t.Errorf("debug=%v: placeholder not substituted", debug)
		}
	}
}
