package app

import (
	"net/http"
	"net/url"
)

func requireMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			applySensitiveNoStoreHeaders(w)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func requireSameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			applySensitiveNoStoreHeaders(w)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func sameOriginRequest(r *http.Request) bool {
	for _, raw := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return false
		}
		return u.Host == r.Host
	}
	return true
}

func adminPostOnly(next http.HandlerFunc) http.HandlerFunc {
	return requireMethod(http.MethodPost, requireSameOrigin(next))
}
