package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func exposition(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

func seriesValue(t *testing.T, name string) string {
	t.Helper()
	for _, line := range strings.Split(exposition(t), "\n") {
		if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	t.Fatalf("series %q not found in exposition", name)
	return ""
}
