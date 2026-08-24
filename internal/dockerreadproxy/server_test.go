package dockerreadproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestProxyAllowsOnlyProjectScopedReads(t *testing.T) {
	var mutex sync.Mutex
	requests := make([]string, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		mutex.Unlock()
		switch {
		case strings.HasSuffix(request.URL.Path, "/containers/json"):
			filters := make(map[string]map[string]bool)
			if err := json.Unmarshal([]byte(request.URL.Query().Get("filters")), &filters); err != nil {
				t.Fatalf("decode filters: %v", err)
			}
			if !filters["label"][composeProjectLabel+"=fixture-project"] {
				t.Fatalf("filters = %#v", filters)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `[]`)
		case strings.HasSuffix(request.URL.Path, "/containers/good/json"):
			_, _ = io.WriteString(response, `{"Config":{"Labels":{"com.docker.compose.project":"fixture-project"}}}`)
		case strings.HasSuffix(request.URL.Path, "/containers/bad/json"):
			_, _ = io.WriteString(response, `{"Config":{"Labels":{"com.docker.compose.project":"other-project"}}}`)
		case strings.HasSuffix(request.URL.Path, "/containers/good/logs"):
			query := request.URL.Query()
			if query.Get("follow") != "0" || query.Get("tail") != "200" || query.Get("stdout") != "1" || query.Get("stderr") != "1" {
				t.Fatalf("log query = %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(response, "fixture log")
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newWithTransport("fixture-project", upstreamURL, http.DefaultTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := proxy.Handler()

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "http://proxy/v1.52/containers/json?all=1", nil))
	if list.Code != http.StatusOK || strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatalf("list response = %d %q", list.Code, list.Body.String())
	}

	logs := httptest.NewRecorder()
	handler.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "http://proxy/v1.52/containers/good/logs?follow=1&tail=all", nil))
	if logs.Code != http.StatusOK || logs.Body.String() != "fixture log" {
		t.Fatalf("logs response = %d %q", logs.Code, logs.Body.String())
	}

	deniedLogs := httptest.NewRecorder()
	handler.ServeHTTP(deniedLogs, httptest.NewRequest(http.MethodGet, "http://proxy/v1.52/containers/bad/logs", nil))
	if deniedLogs.Code != http.StatusForbidden {
		t.Fatalf("outside-project logs status = %d", deniedLogs.Code)
	}

	mutation := httptest.NewRecorder()
	handler.ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "http://proxy/v1.52/containers/good/stop", nil))
	if mutation.Code != http.StatusForbidden {
		t.Fatalf("mutation status = %d", mutation.Code)
	}

	mutex.Lock()
	defer mutex.Unlock()
	for _, request := range requests {
		if strings.Contains(request, "/stop") {
			t.Fatalf("mutation reached upstream: %s", request)
		}
	}
}

func TestProxyRejectsUnscopedPathsAndEncodedSeparators(t *testing.T) {
	proxy, err := newWithTransport("fixture-project", &url.URL{Scheme: "http", Host: "docker"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("rejected request reached upstream")
		return nil, nil
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"http://proxy/v1.52/info",
		"http://proxy/v1.52/containers/good/json",
		"http://proxy/v1.52/volumes",
		"http://proxy/v1.52/containers/good%2Fbad/logs",
	} {
		response := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d", target, response.Code)
		}
	}
}

func TestClassifyAllowsEscapedImageReferencesButNotContainerSeparators(t *testing.T) {
	imageRequest := httptest.NewRequest(http.MethodGet, "http://proxy/v1.52/images/registry.example.com%2Fcpa:tag/json", nil)
	kind, _, identifier, allowed := classifyRequest(imageRequest)
	if !allowed || kind != "image" || identifier != "registry.example.com/cpa:tag" {
		t.Fatalf("image classification = %q %q %v", kind, identifier, allowed)
	}
	containerRequest := httptest.NewRequest(http.MethodGet, "http://proxy/v1.52/containers/good%2Fbad/logs", nil)
	if _, _, _, allowed := classifyRequest(containerRequest); allowed {
		t.Fatal("encoded container separator must remain denied")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
