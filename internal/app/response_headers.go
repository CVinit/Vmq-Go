package app

import "net/http"

func applyCommonSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func applySensitiveNoStoreHeaders(w http.ResponseWriter) {
	applyCommonSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func applyAdminPageHeaders(w http.ResponseWriter) {
	applySensitiveNoStoreHeaders(w)
	w.Header().Set("X-Frame-Options", "DENY")
}
