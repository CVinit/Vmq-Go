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
	if raw := r.Header.Get("Origin"); raw != "" {
		return sameOriginURL(raw, r.Host)
	}
	if raw := r.Header.Get("Referer"); raw != "" {
		return sameOriginURL(raw, r.Host)
	}
	return false
}

func sameOriginURL(raw, host string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == host
}

func adminPostOnly(next http.HandlerFunc) http.HandlerFunc {
	return requireMethod(http.MethodPost, requireSameOrigin(next))
}
