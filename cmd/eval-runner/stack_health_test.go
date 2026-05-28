package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStackHealthReturnsJSONWhenProbesFail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/stack-health", nil)
	rec := httptest.NewRecorder()

	makeStackHealthHandler(stackHealthDeps{})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body stackHealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("Decode(response): %v", err)
	}
	if len(body.Services) == 0 {
		t.Fatal("services is empty, want health rows")
	}
}
