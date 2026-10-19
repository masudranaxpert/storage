package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stream/pkg/cluster"
	"stream/pkg/db"
	"stream/pkg/storage"
	"stream/pkg/web"
)

func TestAuthFlows(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "stream_auth_test_db")
	os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	store, err := db.Open(testDir)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer store.Close()

	coord := cluster.NewCoordinator(10*time.Second, 20*time.Second, store)
	hub := cluster.NewGRPCHub(coord, store, "")
	tiers := storage.NewManager(store)
	srv := web.NewServer(":0", coord, hub, store, tiers, "../../../web/static", "../../../web/templates")
	handler := srv.Handler()

	// 1. Initial status: needs_setup must be true
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	recStatus := httptest.NewRecorder()
	handler.ServeHTTP(recStatus, reqStatus)

	if recStatus.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recStatus.Code)
	}
	var statusRes map[string]interface{}
	json.NewDecoder(recStatus.Body).Decode(&statusRes)
	if statusRes["needs_setup"] != true {
		t.Fatalf("expected needs_setup=true, got %v", statusRes["needs_setup"])
	}

	// 2. Perform setup
	setupBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "supersecretpassword123",
	})
	reqSetup := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(setupBody))
	recSetup := httptest.NewRecorder()
	handler.ServeHTTP(recSetup, reqSetup)

	if recSetup.Code != http.StatusOK {
		t.Fatalf("expected 200 on setup, got %d: %s", recSetup.Code, recSetup.Body.String())
	}

	// Verify session cookie was issued
	var sessionCookie *http.Cookie
	for _, c := range recSetup.Result().Cookies() {
		if c.Name == "stream_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected stream_session cookie to be set after setup")
	}

	// 3. Second setup attempt should fail (409 Conflict)
	reqSetup2 := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(setupBody))
	recSetup2 := httptest.NewRecorder()
	handler.ServeHTTP(recSetup2, reqSetup2)

	if recSetup2.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict on duplicate setup, got %d", recSetup2.Code)
	}

	// 4. Status should now report authenticated with the cookie
	reqStatus2 := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	reqStatus2.AddCookie(sessionCookie)
	recStatus2 := httptest.NewRecorder()
	handler.ServeHTTP(recStatus2, reqStatus2)

	json.NewDecoder(recStatus2.Body).Decode(&statusRes)
	if statusRes["authenticated"] != true || statusRes["username"] != "admin" {
		t.Fatalf("expected authenticated=true, username=admin, got %v", statusRes)
	}

	// 5. Logout
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	reqLogout.AddCookie(sessionCookie)
	recLogout := httptest.NewRecorder()
	handler.ServeHTTP(recLogout, reqLogout)

	if recLogout.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", recLogout.Code)
	}

	// 6. Login with bad credentials -> 401
	badLogin, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "wrongpassword",
	})
	reqBadLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(badLogin))
	recBadLogin := httptest.NewRecorder()
	handler.ServeHTTP(recBadLogin, reqBadLogin)

	if recBadLogin.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on bad credentials, got %d", recBadLogin.Code)
	}

	// 7. Login with valid credentials -> 200
	goodLogin, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "supersecretpassword123",
	})
	reqGoodLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(goodLogin))
	recGoodLogin := httptest.NewRecorder()
	handler.ServeHTTP(recGoodLogin, reqGoodLogin)

	if recGoodLogin.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid login, got %d: %s", recGoodLogin.Code, recGoodLogin.Body.String())
	}
}
