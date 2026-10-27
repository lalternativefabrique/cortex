package fetchpage

import (
	"net/http"
	"os"
	"testing"

	"github.com/lalternativefabrique/searchkit/search/fetch"
)

func TestMain(m *testing.M) {
	fetch.UseHTTPClient(&http.Client{})
	os.Exit(m.Run())
}
