// Package ui serves the embedded single-page browser client.
package ui

import (
	"net/http"
	"strconv"
	"strings"
)

// debugPlaceholder is the token in indexHTML that Handler replaces with the
// server's debug flag.
const debugPlaceholder = "__DEBUG__"

// Handler serves the embedded index page. debug is substituted into the
// page's DEBUG constant: only a debug UI renders thinking text and tool
// output in full.
func Handler(debug bool) http.Handler {
	page := []byte(strings.Replace(indexHTML, debugPlaceholder, strconv.FormatBool(debug), 1))
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
}
