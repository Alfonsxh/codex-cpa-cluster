package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewServesSettingsAndPortalAssetsWithGoOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	server, err := newPreviewServer(
		filepath.Join(root, "testdata", "preview"),
		filepath.Join(root, "frontend", "portal", "public", "assets"),
	)
	if err != nil {
		t.Fatalf("new preview server: %v", err)
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "/portal/assets/codex-cpa-cluster-logo.svg", nil)
	assetResponse := httptest.NewRecorder()
	server.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK ||
		assetResponse.Header().Get("Content-Type") != "image/svg+xml; charset=utf-8" ||
		!strings.Contains(assetResponse.Body.String(), "Codex CPA Cluster") {
		t.Fatalf("portal asset = %d %#v %q", assetResponse.Code, assetResponse.Header(), assetResponse.Body.String())
	}

	for _, path := range []string{
		"/admin/api/onboarding",
		"/admin/api/settings/general",
		"/admin/api/settings/notifications",
		"/admin/api/settings/workspace",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: previewCookie, Value: "mock"})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s = %d %#v %q", path, response.Code, response.Header(), response.Body.String())
		}
		if path == "/admin/api/onboarding" {
			for _, removed := range []string{"first_account", "account_authorization", "first_user"} {
				if strings.Contains(response.Body.String(), removed) {
					t.Fatalf("preview onboarding still contains removed step %q: %s", removed, response.Body.String())
				}
			}
		}
	}
}

func TestPreviewRejectsUnknownPortalAssets(t *testing.T) {
	root := filepath.Join("..", "..")
	server, err := newPreviewServer(
		filepath.Join(root, "testdata", "preview"),
		filepath.Join(root, "frontend", "portal", "public", "assets"),
	)
	if err != nil {
		t.Fatalf("new preview server: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/portal/assets/../private", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unknown asset = %d %#v %q", response.Code, response.Header(), response.Body.String())
	}
}
