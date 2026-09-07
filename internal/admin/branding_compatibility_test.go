package admin

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestBuiltInBrandUpgradePreservesCustomSettingsAcrossAPIs(t *testing.T) {
	for _, test := range []struct {
		name   string
		stored string
		want   string
		custom bool
	}{
		{name: "former built-in", stored: "Codex CPA Cluster", want: "Codex CPA Pool"},
		{name: "current built-in", stored: "Codex CPA Pool", want: "Codex CPA Pool"},
		{name: "custom brand", stored: "Example CPA", want: "Example CPA", custom: true},
		{name: "custom brand containing former name", stored: "Team Codex CPA Cluster", want: "Team Codex CPA Cluster", custom: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, store := newTestAdmin(t)
			ctx := context.Background()
			seed := map[string]any{
				"branding.product_name": test.stored,
				"identity.key_prefix":   "custom_",
				"system.timezone":       "Asia/Shanghai",
			}
			if err := store.WriteSettings(ctx, seed); err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"X-Management-Key": "test-management-key"}
			publicResponse := performAdminRequest(server, http.MethodGet, "/site-config.json", nil, nil, nil)
			if publicResponse.Code != http.StatusOK {
				t.Fatalf("public site = %d %s", publicResponse.Code, publicResponse.Body.String())
			}
			var public publicSiteConfiguration
			decodeAdminResponse(t, publicResponse, &public)
			if public.ProductName != test.want || public.Logo.URL != "/portal/assets/codex-cpa-pool-logo.svg" {
				t.Fatalf("public brand = %#v", public)
			}
			generalResponse := performAdminRequest(server, http.MethodGet, "/admin/api/settings/general", nil, headers, nil)
			if generalResponse.Code != http.StatusOK {
				t.Fatalf("general settings = %d %s", generalResponse.Code, generalResponse.Body.String())
			}
			var general generalSettingsResponse
			decodeAdminResponse(t, generalResponse, &general)
			if general.Values.ProductName != test.want || general.Values.KeyPrefix != "custom_" || brandingCustomized(general.Values) != test.custom {
				t.Fatalf("general brand = %#v", general.Values)
			}
			catalogResponse := performAdminRequest(server, http.MethodGet, "/admin/api/settings/configuration", nil, headers, nil)
			if catalogResponse.Code != http.StatusOK {
				t.Fatalf("configuration = %d %s", catalogResponse.Code, catalogResponse.Body.String())
			}
			var catalog configurationCatalogResponse
			decodeAdminResponse(t, catalogResponse, &catalog)
			found := false
			for _, group := range catalog.Groups {
				for _, field := range group.Fields {
					if field.Key == "branding.product_name" {
						found = true
						if field.Value != test.want || field.Default != "Codex CPA Pool" {
							t.Fatalf("configuration brand = %#v", field)
						}
					}
				}
			}
			if !found {
				t.Fatal("configuration brand is missing")
			}
			after, err := store.ReadSettings(ctx)
			if err != nil || !reflect.DeepEqual(after, seed) {
				t.Fatalf("reading branding changed stored settings: %#v, %v", after, err)
			}
		})
	}
}

func TestSavingFormerBuiltInBrandPersistsPoolName(t *testing.T) {
	for _, endpoint := range []string{"general", "configuration"} {
		t.Run(endpoint, func(t *testing.T) {
			server, store := newTestAdmin(t)
			ctx := context.Background()
			if err := store.WriteSettings(ctx, map[string]any{
				"branding.product_name": "Codex CPA Cluster",
				"identity.key_prefix":   "custom_",
				"system.timezone":       "Asia/Shanghai",
			}); err != nil {
				t.Fatal(err)
			}
			var values any = map[string]any{"branding.product_name": "Codex CPA Cluster"}
			method := http.MethodPost
			if endpoint == "general" {
				method = http.MethodPut
				general := defaultGeneralSettings()
				general.ProductName = "Codex CPA Cluster"
				general.KeyPrefix = "custom_"
				values = general
			}
			response := performAdminRequest(server, method, "/admin/api/settings/"+endpoint, map[string]any{
				"confirm": "save", "values": values,
			}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("save brand = %d %s", response.Code, response.Body.String())
			}
			stored, err := store.ReadSettings(ctx)
			if err != nil || stored["branding.product_name"] != "Codex CPA Pool" || stored["identity.key_prefix"] != "custom_" {
				t.Fatalf("saved brand = %#v, %v", stored, err)
			}
		})
	}
}
