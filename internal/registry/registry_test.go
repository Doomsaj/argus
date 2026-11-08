package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuthWithoutOIDC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "argus" || p != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"versions":[]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	c.User, c.Password = "argus", "s3cret"
	if _, err := c.VersionExists(t.Context(), "g", "a", "v"); err != nil {
		t.Fatalf("with basic auth: %v", err)
	}
	if _, err := New(srv.URL, nil).VersionExists(t.Context(), "g", "a", "v"); err == nil {
		t.Fatal("without credentials: want error from 401")
	}
}
