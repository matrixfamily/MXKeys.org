package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mxkeys/internal/keys"
)

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	data := map[string]string{"key": "value"}

	err := writeJSON(rr, data)
	if err != nil {
		t.Fatalf("writeJSON failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if result["key"] != "value" {
		t.Errorf("expected value, got %s", result["key"])
	}
}

type matrixError struct {
	ErrCode string `json:"errcode"`
	Error   string `json:"error"`
}

func parseMatrixError(body []byte) (*matrixError, error) {
	var e matrixError
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func TestKeyQueryBadJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/_matrix/key/v2/query", strings.NewReader("{invalid}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{
				"errcode": "M_BAD_JSON",
				"error":   "Invalid JSON",
			})
			return
		}
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}

	e, err := parseMatrixError(rr.Body.Bytes())
	if err != nil {
		t.Fatalf("failed to parse error: %v", err)
	}

	if e.ErrCode != "M_BAD_JSON" {
		t.Errorf("expected M_BAD_JSON, got %s", e.ErrCode)
	}
}

func TestKeyQueryEmptyServerKeys(t *testing.T) {
	body := `{"server_keys": {}}`
	req := httptest.NewRequest("POST", "/_matrix/key/v2/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ServerKeys map[string]interface{} `json:"server_keys"`
		}
		json.NewDecoder(r.Body).Decode(&request)

		if len(request.ServerKeys) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{
				"errcode": "M_BAD_JSON",
				"error":   "No servers specified",
			})
			return
		}
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestKeyQueryTooManyServers(t *testing.T) {
	servers := make(map[string]interface{})
	for i := 0; i < 101; i++ {
		serverName := fmt.Sprintf("server%d.example.com", i)
		servers[serverName] = map[string]interface{}{}
	}

	requestData := map[string]interface{}{"server_keys": servers}
	body, err := json.Marshal(requestData)
	if err != nil {
		t.Fatalf("failed to marshal requestData: %v", err)
	}

	req := httptest.NewRequest("POST", "/_matrix/key/v2/query", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	maxServers := 100

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ServerKeys map[string]interface{} `json:"server_keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if len(request.ServerKeys) > maxServers {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{
				"errcode": "M_BAD_JSON",
				"error":   "Too many servers",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for >100 servers, got %d (servers count: %d)", rr.Code, len(servers))
	}
}

func TestKeyQueryInvalidServerName(t *testing.T) {
	body := `{"server_keys": {"../etc/passwd": {}}}`
	req := httptest.NewRequest("POST", "/_matrix/key/v2/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ServerKeys map[string]interface{} `json:"server_keys"`
		}
		json.NewDecoder(r.Body).Decode(&request)

		for serverName := range request.ServerKeys {
			if err := ValidateServerName(serverName, 255); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{
					"errcode": "M_INVALID_PARAM",
					"error":   "Invalid server name",
				})
				return
			}
		}
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid server name, got %d", rr.Code)
	}

	e, _ := parseMatrixError(rr.Body.Bytes())
	if e.ErrCode != "M_INVALID_PARAM" {
		t.Errorf("expected M_INVALID_PARAM, got %s", e.ErrCode)
	}
}

func TestKeyQueryInvalidKeyID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID := r.URL.Query().Get("key_id")
		if keyID != "" {
			if err := ValidateKeyID(keyID); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]string{
					"errcode": "M_INVALID_PARAM",
					"error":   "Invalid key ID",
				})
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/_matrix/key/v2/server/rsa:invalid", nil)
	rr := httptest.NewRecorder()

	if err := ValidateKeyID("rsa:invalid"); err == nil {
		t.Fatal("rsa:invalid should be rejected by ValidateKeyID")
	}

	req = httptest.NewRequest("GET", "/_matrix/key/v2/server?key_id=rsa:invalid", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid key ID, got %d", rr.Code)
	}
}

func TestHealthEndpointStructure(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]interface{}{
			"status":  "healthy",
			"server":  "test.server",
			"version": "0.1.0",
		})
	})

	req := httptest.NewRequest("GET", "/_mxkeys/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result["status"] != "healthy" {
		t.Error("missing or wrong status")
	}
	if result["version"] == nil {
		t.Error("missing version")
	}
}

func TestLivenessEndpoint(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, map[string]string{"status": "alive"})
	})

	req := httptest.NewRequest("GET", "/_mxkeys/live", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var result map[string]string
	json.Unmarshal(rr.Body.Bytes(), &result)
	if result["status"] != "alive" {
		t.Error("expected alive status")
	}
}

func TestStatusEndpointStructure(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]interface{}{
			"status":  "ok",
			"version": "0.1.0",
			"uptime":  "1h2m3s",
			"cache": map[string]int{
				"memory_entries":   10,
				"database_entries": 100,
			},
			"database": map[string]int{
				"open_connections": 5,
			},
		})
	})

	req := httptest.NewRequest("GET", "/_mxkeys/status", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	required := []string{"status", "version", "uptime", "cache", "database"}
	for _, field := range required {
		if result[field] == nil {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestContentTypeJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestDecodeStrictJSONSingleObject(t *testing.T) {
	var out map[string]interface{}
	err := decodeStrictJSON(strings.NewReader(`{"server_keys":{"example.org":{}}}`), &out)
	if err != nil {
		t.Fatalf("decodeStrictJSON failed: %v", err)
	}
}

func TestDecodeStrictJSONTrailingData(t *testing.T) {
	var out map[string]interface{}
	err := decodeStrictJSON(strings.NewReader(`{"server_keys":{"example.org":{}}} {"extra":1}`), &out)
	if err == nil {
		t.Fatal("expected trailing JSON error")
	}
}

func TestDecodeStrictJSONMaxBytesError(t *testing.T) {
	rec := httptest.NewRecorder()
	body := http.MaxBytesReader(rec, io.NopCloser(strings.NewReader(`{"server_keys":{"example.org":{}}}`)), 8)
	defer body.Close()

	var out map[string]interface{}
	err := decodeStrictJSON(body, &out)
	if err == nil {
		t.Fatal("expected max body size error")
	}

	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("expected max bytes error, got: %v", err)
	}
}

func TestLiveFederationQueryStrictness(t *testing.T) {
	if os.Getenv("MXKEYS_LIVE_TEST") != "1" {
		t.Skip("set MXKEYS_LIVE_TEST=1 to run live federation checks")
	}

	baseURL := os.Getenv("MXKEYS_LIVE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://mxkeys.org"
	}

	client := &http.Client{Timeout: 15 * time.Second}

	reqBody := `{"server_keys":{"s-a.mxtest.tech":{},"s-b.mxtest.tech":{}}}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/_matrix/key/v2/query", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("live query failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("live query returned %d: %s", resp.StatusCode, string(body))
	}

	var queryResp struct {
		ServerKeys []map[string]interface{} `json:"server_keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queryResp); err != nil {
		t.Fatalf("failed to decode live response: %v", err)
	}
	if len(queryResp.ServerKeys) < 1 {
		t.Fatalf("expected at least 1 server_keys entry, got %d", len(queryResp.ServerKeys))
	}

	trailingReqBody := `{"server_keys":{"s-a.mxtest.tech":{}}}{"x":1}`
	trailingReq, err := http.NewRequest(http.MethodPost, baseURL+"/_matrix/key/v2/query", strings.NewReader(trailingReqBody))
	if err != nil {
		t.Fatalf("failed to build trailing request: %v", err)
	}
	trailingReq.Header.Set("Content-Type", "application/json")

	trailingResp, err := client.Do(trailingReq)
	if err != nil {
		t.Fatalf("live trailing query failed: %v", err)
	}
	defer trailingResp.Body.Close()
	if trailingResp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(trailingResp.Body)
		t.Fatalf("expected non-200 for trailing JSON, got 200: %s", string(body))
	}
}

func TestLiveQueryCompatibility(t *testing.T) {
	if os.Getenv("MXKEYS_LIVE_TEST") != "1" {
		t.Skip("set MXKEYS_LIVE_TEST=1 to run live federation checks")
	}

	baseURL := os.Getenv("MXKEYS_LIVE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://mxkeys.org"
	}

	client := &http.Client{Timeout: 15 * time.Second}
	reqBody := `{"server_keys":{"s-a.mxtest.tech":{},"s-b.mxtest.tech":{}}}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/_matrix/key/v2/query", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("live query failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("live query returned %d: %s", resp.StatusCode, string(body))
	}

	var queryResp struct {
		ServerKeys []struct {
			ServerName string                            `json:"server_name"`
			VerifyKeys map[string]keys.VerifyKeyResponse `json:"verify_keys"`
			Signatures map[string]map[string]string      `json:"signatures"`
		} `json:"server_keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queryResp); err != nil {
		t.Fatalf("failed to decode live response: %v", err)
	}
	if len(queryResp.ServerKeys) < 1 {
		t.Fatalf("expected at least 1 server_keys entry, got %d", len(queryResp.ServerKeys))
	}
	for _, entry := range queryResp.ServerKeys {
		if entry.ServerName == "" {
			t.Fatal("server_name must not be empty")
		}
		if len(entry.VerifyKeys) == 0 {
			t.Fatalf("verify_keys must not be empty for %s", entry.ServerName)
		}
		if len(entry.Signatures) == 0 {
			t.Fatalf("signatures must not be empty for %s", entry.ServerName)
		}
	}
}

func TestLiveNotaryFailurePath(t *testing.T) {
	if os.Getenv("MXKEYS_LIVE_TEST") != "1" {
		t.Skip("set MXKEYS_LIVE_TEST=1 to run live federation checks")
	}

	baseURL := os.Getenv("MXKEYS_LIVE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://mxkeys.org"
	}

	client := &http.Client{Timeout: 20 * time.Second}
	reqBody := `{"server_keys":{"s-a.mxtest.tech":{},"no-such-server.invalid":{}}}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/_matrix/key/v2/query", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("live query failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("live query returned %d: %s", resp.StatusCode, string(body))
	}

	var queryResp struct {
		ServerKeys []struct {
			ServerName string `json:"server_name"`
		} `json:"server_keys"`
		Failures map[string]interface{} `json:"failures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queryResp); err != nil {
		t.Fatalf("failed to decode live response: %v", err)
	}
	if len(queryResp.ServerKeys) == 0 {
		t.Fatal("expected at least one successful server_keys entry")
	}
	if _, ok := queryResp.Failures["no-such-server.invalid"]; !ok {
		t.Fatalf("expected failure entry for no-such-server.invalid, got: %#v", queryResp.Failures)
	}
}

func TestValidateKeyQueryServerKeys(t *testing.T) {
	valid := map[string]map[string]keys.KeyCriteria{
		"s-a.mxtest.tech": {
			"ed25519:keyA": {MinimumValidUntilTS: 0},
		},
		"s-b.mxtest.tech": {},
	}
	if err := validateKeyQueryServerKeys(valid, 255); err != nil {
		t.Fatalf("expected valid server_keys, got error: %v", err)
	}
}

func TestValidateKeyQueryServerKeysRejectsInvalidKeyID(t *testing.T) {
	invalid := map[string]map[string]keys.KeyCriteria{
		"s-a.mxtest.tech": {
			"rsa:bad": {},
		},
	}
	if err := validateKeyQueryServerKeys(invalid, 255); err == nil {
		t.Fatal("expected invalid key ID error")
	}
}

func TestValidateKeyQueryServerKeysRejectsNegativeMinValidUntil(t *testing.T) {
	invalid := map[string]map[string]keys.KeyCriteria{
		"s-a.mxtest.tech": {
			"ed25519:keyA": {MinimumValidUntilTS: -1},
		},
	}
	if err := validateKeyQueryServerKeys(invalid, 255); err == nil {
		t.Fatal("expected negative minimum_valid_until_ts error")
	}
}

func TestValidateKeyQueryServerKeysRejectsEmptyKeyID(t *testing.T) {
	invalid := map[string]map[string]keys.KeyCriteria{
		"s-a.mxtest.tech": {
			"": {},
		},
	}
	if err := validateKeyQueryServerKeys(invalid, 255); err == nil {
		t.Fatal("expected empty key ID error")
	}
}

func TestValidateKeyQueryServerKeysRejectsUnicodeHostname(t *testing.T) {
	invalid := map[string]map[string]keys.KeyCriteria{
		"пример.рф": {
			"ed25519:keyA": {},
		},
	}
	if err := validateKeyQueryServerKeys(invalid, 255); err == nil {
		t.Fatal("expected unicode hostname rejection (must be punycode)")
	}
}
