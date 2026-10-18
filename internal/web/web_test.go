package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	handler := handlerFor(fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	})
	for _, tc := range []struct {
		path, body, cache string
		status            int
	}{
		{"/", "<html>app</html>", "no-cache", http.StatusOK},
		{"/trade/ATHR", "<html>app</html>", "no-cache", http.StatusOK},
		{"/assets/app-1.js", "console.log(1)", "public, max-age=31536000, immutable", http.StatusOK},
		{"/favicon.svg", "<svg/>", "", http.StatusOK},
		{"/missing.png", "", "", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != tc.status || (tc.body != "" && recorder.Body.String() != tc.body) || recorder.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("GET %s = %d %q cache=%q", tc.path, recorder.Code, recorder.Body.String(), recorder.Header().Get("Cache-Control"))
		}
	}
}

func TestHandlerExplainsMissingBuild(t *testing.T) {
	recorder := httptest.NewRecorder()
	handlerFor(fstest.MapFS{".gitkeep": {}}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unbuilt UI = %d; want 503", recorder.Code)
	}
}
