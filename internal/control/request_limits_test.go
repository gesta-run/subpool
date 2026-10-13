package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestGlobalRequestLimitsSettings(t *testing.T) {
	server, st, _ := newControlServer(t)
	server.WithRequestBodyDefaults(128<<20, 512<<20)
	mux := http.NewServeMux()
	server.Register(mux)
	cookie := loginCookie(t, mux)
	call := func(method, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body))
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	initial := call(http.MethodGet, "")
	var settings domain.Settings
	if err := json.Unmarshal(initial.Body.Bytes(), &settings); err != nil || settings.MaxRequestBodyBytes != 128<<20 || settings.MaxInflightRequestBodyBytes != 512<<20 {
		t.Fatalf("deployment defaults not exposed: %s", initial.Body.String())
	}
	saved := call(http.MethodPut, `{"max_api_keys_per_account":4,"max_request_body_bytes":268435456,"max_inflight_request_body_bytes":1073741824}`)
	if saved.Code != http.StatusOK || st.settings.MaxRequestBodyBytes != 256<<20 || st.settings.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatalf("request limits not persisted: %d %s", saved.Code, saved.Body.String())
	}
	updated := call(http.MethodPut, `{"max_api_keys_per_account":5}`)
	if updated.Code != http.StatusOK || st.settings.MaxAPIKeysPerAccount != 5 || st.settings.MaxRequestBodyBytes != 256<<20 || st.settings.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatal("capacity-only updates replaced saved request limits")
	}
	for _, test := range []struct{ max, budget int64 }{{0, 1024}, {256, 1023}, {-1, 1024}, {9223372036854775807, 9223372036854775807}} {
		invalid := call(http.MethodPut, fmt.Sprintf(`{"max_api_keys_per_account":5,"max_request_body_bytes":%d,"max_inflight_request_body_bytes":%d}`, test.max, test.budget))
		if invalid.Code != http.StatusBadRequest || st.settings.MaxRequestBodyBytes != 256<<20 || st.settings.MaxInflightRequestBodyBytes != 1<<30 {
			t.Fatalf("invalid limits changed settings: %d %s", invalid.Code, invalid.Body.String())
		}
	}
	partial := call(http.MethodPut, `{"max_api_keys_per_account":6,"max_request_body_bytes":134217729}`)
	if partial.Code != http.StatusOK || st.settings.MaxAPIKeysPerAccount != 6 || st.settings.MaxRequestBodyBytes != (128<<20)+1 || st.settings.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatalf("partial update did not preserve the latest budget: %d %s", partial.Code, partial.Body.String())
	}
	invalidPartial := call(http.MethodPut, `{"max_api_keys_per_account":7,"max_inflight_request_body_bytes":536870912}`)
	if invalidPartial.Code != http.StatusBadRequest || st.settings.MaxAPIKeysPerAccount != 6 || st.settings.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatalf("invalid partial update was not rolled back: %d %s", invalidPartial.Code, invalidPartial.Body.String())
	}
}

func TestGlobalRequestLimitsCapacityPreservesEnvironmentFallback(t *testing.T) {
	server, st, _ := newControlServer(t)
	server.WithRequestBodyDefaults(128<<20, 512<<20)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"max_api_keys_per_account":5}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var settings domain.Settings
	if err := json.Unmarshal(recorder.Body.Bytes(), &settings); err != nil || recorder.Code != http.StatusOK ||
		settings.MaxRequestBodyBytes != 128<<20 || st.settings.MaxRequestBodyBytes != 0 || st.settings.MaxInflightRequestBodyBytes != 0 {
		t.Fatalf("capacity-only save froze deployment defaults: %d %s", recorder.Code, recorder.Body.String())
	}
}
