package control

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// instrumentHTTP records a request by method, matched route pattern and status.
func TestInstrumentHTTPRecordsByRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := instrumentHTTP(mux)

	before := testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/ping/{id}", "204"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ping/42", nil))
	after := testutil.ToFloat64(httpRequests.WithLabelValues("GET", "/ping/{id}", "204"))

	require.Equal(t, before+1, after, "counts by the bounded route pattern and the observed status")
}
