package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.digikala.com/argus/argus/internal/pactbroker"
	"git.digikala.com/argus/argus/internal/registry"
)

const deployedSHA = "aaaaaaaaaaaa0000000000000000000000000000"

func TestReconcileAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"t","expires_in":300}`)
	})
	mux.HandleFunc("GET /reg/search/artifacts", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"count":2,"artifacts":[{"groupId":"catalog","artifactId":"product-detail"},{"groupId":"x","artifactId":"Product_Detail"}]}`)
	})
	mux.HandleFunc("GET /reg/groups/catalog/artifacts/product-detail/branches/production/versions", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"count":1,"versions":[{"version":"bbbbbbbbbbbb"}]}`)
	})
	mux.HandleFunc("GET /reg/groups/catalog/artifacts/product-detail/versions/bbbbbbbbbbbb", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"labels":{"git.sha":"bbbbbbbbbbbb1111"}}`)
	})
	mux.HandleFunc("GET /reg/groups/catalog/artifacts/product-detail/versions/bbbbbbbbbbbb/content", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("GET /reg/groups/catalog/artifacts/product-detail/versions/aaaaaaaaaaaa", http.NotFound)
	mux.HandleFunc("GET /pb/pacticipants", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"_embedded":{"pacticipants":[{"name":"product-detail"},{"name":"marcopolo"}]}}`)
	})
	mux.HandleFunc("GET /pb/environments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"_embedded":{"environments":[{"uuid":"u1","name":"production"}]}}`)
	})
	mux.HandleFunc("GET /pb/environments/u1/deployed-versions/currently-deployed", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"_embedded":{"deployedVersions":[
		  {"_embedded":{"pacticipant":{"name":"product-detail"},"version":{"number":%q}}},
		  {"_embedded":{"pacticipant":{"name":"marcopolo"},"version":{"number":"cccccccccccc"}}}]}}`, deployedSHA)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts := &registry.TokenSource{TokenURL: srv.URL + "/token", HTTP: srv.Client()}
	got, err := reconcileAll(t.Context(), registry.New(srv.URL+"/reg", ts), pactbroker.NewHTTP(srv.URL+"/pb", "u", "p"), []string{"production"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"deployed-differs/product-detail": true, "near-duplicate/Product_Detail": true,
		"no-spec-deployed/product-detail": true, "only-in-apicurio/Product_Detail": true,
		"only-in-broker/marcopolo": true,
	}
	for _, m := range got {
		if !want[m.Kind+"/"+m.Service] {
			t.Errorf("unexpected %+v", m)
		}
		delete(want, m.Kind+"/"+m.Service)
	}
	if len(want) > 0 {
		t.Errorf("missing %v (got %+v)", want, got)
	}
}
