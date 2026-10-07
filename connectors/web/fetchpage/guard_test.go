package fetchpage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lalternative/packages/go/cortex/tools"
	"github.com/lalternative/packages/go/search/fetch"
)

func TestLocalFallbackRefusesInternalAddressByDefault(t *testing.T) {
	fetch.UseHTTPClient(nil)
	t.Cleanup(func() { fetch.UseHTTPClient(&http.Client{}) })

	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the local fallback reached a loopback address")
	}))
	defer origin.Close()

	if _, err := New(Config{}).Fetch(context.Background(), tools.FetchRequest{URL: origin.URL}); err == nil {
		t.Fatal("Fetch read a loopback address")
	}
}

func TestNoLocalFallbackWithoutTornad(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the disabled fallback fetched the page")
	}))
	defer origin.Close()

	_, err := New(Config{NoLocalFallback: true}).Fetch(context.Background(), tools.FetchRequest{URL: origin.URL})
	if !errors.Is(err, errNoReader) {
		t.Fatalf("err = %v, want errNoReader", err)
	}
}

func TestNoLocalFallbackWhenTornadIsRefused(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the disabled fallback fetched the page")
	}))
	defer origin.Close()
	tornad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream refused"}`))
	}))
	defer tornad.Close()

	_, err := New(Config{BaseURL: tornad.URL, NoLocalFallback: true}).Fetch(context.Background(), tools.FetchRequest{URL: origin.URL})
	if err == nil {
		t.Fatal("Fetch succeeded with the fallback disabled")
	}
}
