package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterWebServesPrecompressedImmutableAssets(t *testing.T) {
	dist := t.TempDir()
	assets := filepath.Join(dist, "assets")
	if err := os.Mkdir(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("console.log('subpool');", 100)
	asset := filepath.Join(assets, "app-123.js")
	if err := os.WriteFile(asset, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	compressed, err := os.Create(asset + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(compressed)
	if _, err = writer.Write([]byte(source)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SUBPOOL_WEB_DIR", dist)
	mux := http.NewServeMux()
	registerWeb(mux)
	request := httptest.NewRequest(http.MethodGet, "/assets/app-123.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
	if cache := recorder.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", cache)
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != source {
		t.Fatal("compressed response did not contain the source asset")
	}
}

func TestRegisterWebFallsBackToUncompressedAsset(t *testing.T) {
	dist := t.TempDir()
	assets := filepath.Join(dist, "assets")
	if err := os.Mkdir(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app-123.js"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBPOOL_WEB_DIR", dist)
	mux := http.NewServeMux()
	registerWeb(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/app-123.js", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "asset" || recorder.Header().Get("Content-Encoding") != "" {
		t.Fatalf("status=%d encoding=%q body=%q", recorder.Code, recorder.Header().Get("Content-Encoding"), recorder.Body.String())
	}
}

func TestRegisterWebDoesNotCacheDirectHTML(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("app shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUBPOOL_WEB_DIR", dist)
	mux := http.NewServeMux()
	registerWeb(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if recorder.Code != http.StatusMovedPermanently || recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("status=%d Cache-Control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
}

func TestAcceptsGzipHonorsQuality(t *testing.T) {
	for _, test := range []struct {
		header string
		want   bool
	}{
		{header: "br, gzip", want: true},
		{header: "gzip; q=0.5", want: true},
		{header: "gzip; q=0", want: false},
		{header: "br", want: false},
	} {
		if got := acceptsGzip(test.header); got != test.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", test.header, got, test.want)
		}
	}
}
