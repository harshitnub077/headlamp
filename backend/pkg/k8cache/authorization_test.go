// Copyright 2025 The Kubernetes Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8cache_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/mux"
	"github.com/kubernetes-sigs/headlamp/backend/pkg/k8cache"
	"github.com/kubernetes-sigs/headlamp/backend/pkg/kubeconfig"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

type MockKubeConfig struct {
	*kubeconfig.Context
}

func (k *MockKubeConfig) ClientSetWithToken(token string) (kubernetes.Interface, error) {
	return fake.NewClientset(), nil
}

type MockClientConfig struct{}

func (k *MockKubeConfig) ClientConfig() (clientcmd.ClientConfig, error) {
	conf := api.Config{
		Clusters: map[string]*api.Cluster{
			k.KubeContext.Cluster: k.Cluster,
		},
		AuthInfos: map[string]*api.AuthInfo{
			k.KubeContext.AuthInfo: k.AuthInfo,
		},
		Contexts: map[string]*api.Context{
			k.Name: k.KubeContext,
		},
	}

	return clientcmd.NewNonInteractiveClientConfig(conf, "kind-headlamp-admin", nil, nil), nil
}

func TestGetClientSet(t *testing.T) {
	tests := []struct {
		name               string
		mockK              MockKubeConfig
		headlampContextKey string
		token              string
		clientSet          *kubernetes.Clientset
		expectedError      error
	}{
		{
			name: "valid ClusterID returns cached clientset",
			mockK: MockKubeConfig{
				&kubeconfig.Context{
					ClusterID:   "/home/user/.kubeconfig+kind-headlamp-admin",
					Cluster:     &api.Cluster{Server: "https://example.com"},
					AuthInfo:    &api.AuthInfo{Token: "abcdef"},
					KubeContext: &api.Context{Cluster: "kind-headlamp-admin"},
				},
			},
			headlampContextKey: "kind-headlamp-admin",
			token:              "token-1245",
			expectedError:      nil,
		},
		{
			name: "return unexpected ClusterID format",
			mockK: MockKubeConfig{
				&kubeconfig.Context{
					ClusterID:   "/home/user/.kubeconfig/kind-headlamp-admin",
					Cluster:     &api.Cluster{Server: "https://example.com"},
					AuthInfo:    &api.AuthInfo{Token: "abcdef"},
					KubeContext: &api.Context{Cluster: "kind-headlamp-admin"},
				},
			},
			headlampContextKey: "kind-headlamp-admin",
			token:              "token-54321",
			expectedError: fmt.Errorf("unexpected ClusterID format in GetClientSet: " +
				"\"/home/user/.kubeconfig/kind-headlamp-admin\""),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := k8cache.GetClientSet(tc.headlampContextKey, tc.mockK.Context, tc.token)
			if tc.clientSet != nil { // It is difficult to compare the expected clientset with
				// the returned clientSet as it return nested-struct inside the clientset which
				// returns only memory references. To check whether the clientset was correct or
				// not we can check by checking whether there was an error, so if there was an
				// error clientset will be empty.
				assert.NotEmpty(t, cs)
				assert.NoError(t, err)
			} else {
				assert.Equal(t, tc.expectedError, err)
			}
		})
	}
}

func TestGetKindAndVerb(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		urlPath      string
		muxVars      map[string]string
		expectedKind string
		expectedVerb string
	}{
		{
			name:         "Core API with no trailing slash",
			method:       "GET",
			urlPath:      "/api/v1/pods",
			muxVars:      map[string]string{"api": "api/v1/pods"},
			expectedKind: "pods",
			expectedVerb: "list",
		},
		{
			name:         "Named API with trailing slash",
			method:       "GET",
			urlPath:      "/apis/apps/v1/deployments/",
			muxVars:      map[string]string{"api": "apis/apps/v1/deployments"},
			expectedKind: "deployments",
			expectedVerb: "list",
		},
		{
			name:         "POST request",
			method:       "POST",
			urlPath:      "/api/v1/pods",
			muxVars:      map[string]string{"api": "api/v1/pods"},
			expectedKind: "pods",
			expectedVerb: "unknown",
		},
		{
			name:         "Watch request for a named API",
			method:       "GET",
			urlPath:      "/apis/apps/v1/deployments?watch=1",
			muxVars:      map[string]string{"api": "apis/apps/v1/deployments"},
			expectedKind: "deployments",
			expectedVerb: "watch",
		},
		{
			name:         "Watch query set to 0 (false)",
			method:       "GET",
			urlPath:      "/apis/apps/v1/deployments?watch=0",
			muxVars:      map[string]string{"api": "apis/apps/v1/deployments"},
			expectedKind: "deployments",
			expectedVerb: "list",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.urlPath, nil)
			req = mux.SetURLVars(req, tc.muxVars)
			kind, verb := k8cache.GetKindAndVerb(req)
			assert.Equal(t, tc.expectedKind, kind)
			assert.Equal(t, tc.expectedVerb, verb)
		})
	}
}

type getKindAndVerbTestCase struct {
	name         string
	urlPath      string
	muxVars      map[string]string
	expectedKind string
	expectedVerb string
}

func TestGetKindAndVerbNamedResources(t *testing.T) {
	tests := []getKindAndVerbTestCase{
		{
			name:         "Core API named namespaced resource",
			urlPath:      "/api/v1/namespaces/default/pods/nginx",
			muxVars:      map[string]string{"api": "api/v1/namespaces/default/pods/nginx"},
			expectedKind: "pods",
			expectedVerb: "get",
		},
		{
			name:         "Named API named namespaced resource",
			urlPath:      "/apis/apps/v1/namespaces/default/deployments/frontend",
			muxVars:      map[string]string{"api": "apis/apps/v1/namespaces/default/deployments/frontend"},
			expectedKind: "deployments",
			expectedVerb: "get",
		},
		{
			name:         "Core API named resource subresource",
			urlPath:      "/api/v1/namespaces/default/pods/nginx/log",
			muxVars:      map[string]string{"api": "api/v1/namespaces/default/pods/nginx/log"},
			expectedKind: "pods",
			expectedVerb: "get",
		},
	}

	runGetKindAndVerbTests(t, tests)
}

func TestGetKindAndVerbNamespaceResources(t *testing.T) {
	tests := []getKindAndVerbTestCase{
		{
			name:         "Core API namespace list",
			urlPath:      "/api/v1/namespaces",
			muxVars:      map[string]string{"api": "api/v1/namespaces"},
			expectedKind: "namespaces",
			expectedVerb: "list",
		},
		{
			name:         "Core API named namespace",
			urlPath:      "/api/v1/namespaces/default",
			muxVars:      map[string]string{"api": "api/v1/namespaces/default"},
			expectedKind: "namespaces",
			expectedVerb: "get",
		},
		{
			name:         "Core API namespace status subresource",
			urlPath:      "/api/v1/namespaces/default/status",
			muxVars:      map[string]string{"api": "api/v1/namespaces/default/status"},
			expectedKind: "namespaces",
			expectedVerb: "get",
		},
		{
			name:         "Core API namespace finalize subresource",
			urlPath:      "/api/v1/namespaces/default/finalize",
			muxVars:      map[string]string{"api": "api/v1/namespaces/default/finalize"},
			expectedKind: "namespaces",
			expectedVerb: "get",
		},
	}

	runGetKindAndVerbTests(t, tests)
}

func runGetKindAndVerbTests(t *testing.T, tests []getKindAndVerbTestCase) {
	t.Helper()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.urlPath, nil)
			req = mux.SetURLVars(req, tc.muxVars)
			kind, verb := k8cache.GetKindAndVerb(req)
			assert.Equal(t, tc.expectedKind, kind)
			assert.Equal(t, tc.expectedVerb, verb)
		})
	}
}

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name      string
		urlObj    *url.URL
		token     string
		mockK     MockKubeConfig
		isAllowed bool
	}{
		{
			name:   "user is not allowed",
			urlObj: &url.URL{Path: "/clusters/kind-headlamp-admin/api/v1/pods"},
			token:  "token-example",
			mockK: MockKubeConfig{
				&kubeconfig.Context{
					ClusterID: "/home/saurav/.kubeconfig+kind-headlamp-admin",
					Cluster: &api.Cluster{
						Server: "https://example.com",
					},
					AuthInfo: &api.AuthInfo{
						Token: "abcdef",
					},
					KubeContext: &api.Context{
						Cluster: "kind-headlamp-admin",
					},
				},
			},
			isAllowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.urlObj.Path, nil)

			_, err := tc.mockK.ClientSetWithToken(tc.token)
			_, _ = tc.mockK.ClientConfig()

			assert.NoError(t, err)

			isAllowed, _ := k8cache.IsAllowed("kind-headlamp-admin", tc.mockK.Context, r)
			assert.Equal(t, tc.isAllowed, isAllowed)
		})
	}
}

// ServeFromCacheOrForwardToK8s test whether it is returning from the cache if availables or
// storing the in cache when getting error while authorizing user.
func TestServeFromCacheOrForwardToK8s(t *testing.T) {
	t.Run("stores on miss and serves on hit", func(t *testing.T) {
		urlObj := url.URL{Path: "/clusters/kind-headlamp-admin/api/v1/pods"}
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, urlObj.Path, nil)
		cache := NewMockCache()

		// First call should MISS the cache and invoke next.
		w1 := httptest.NewRecorder()
		rcw1 := k8cache.NewResponseCapture(w1)
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot) // example response
			_, err := w.Write([]byte("next handler called"))
			assert.NoError(t, err)
		})

		k8cache.ServeFromCacheOrForwardToK8s(cache, true, next, "key", w1, r, rcw1)

		// Assert first call went through next handler
		assert.Equal(t, http.StatusTeapot, w1.Code)
		assert.Contains(t, w1.Body.String(), "next handler called")
		// Assert response was stored in cache
		val, err := cache.Get(context.Background(), "key")
		assert.NoError(t, err, "expected key to be stored in cache")
		assert.NotEmpty(t, val)

		// Second call should HIT the cache and skip next
		called := false
		nextNoCall := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true

			w.WriteHeader(http.StatusInternalServerError)
		})

		w2 := httptest.NewRecorder()
		rcw2 := k8cache.NewResponseCapture(w2)
		k8cache.ServeFromCacheOrForwardToK8s(cache, true, nextNoCall, "key", w2, r, rcw2)

		// Assert second call did not invoke next
		assert.False(t, called, "expected cache hit to skip next handler")

		// Assert second call's output matches what was cached
		assert.Equal(t, http.StatusTeapot, w2.Code)
		assert.Contains(t, w2.Body.String(), "next handler called")
	})
}

type testRoundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (t *testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.fn(req)
}

func TestIsAllowedCaching(t *testing.T) {
	k8cache.ResetForTesting()

	mockK := MockKubeConfig{
		&kubeconfig.Context{
			ClusterID:   "/home/user/.kubeconfig+kind-headlamp-admin",
			Cluster:     &api.Cluster{Server: "https://example.com"},
			AuthInfo:    &api.AuthInfo{Token: "abcdef"},
			KubeContext: &api.Context{Cluster: "kind-headlamp-admin"},
		},
	}

	jsonResp := `{
		"apiVersion": "authorization.k8s.io/v1",
		"kind": "SelfSubjectAccessReview",
		"status": {
			"allowed": true
		}
	}`

	var calls int
	rt := &testRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: 201,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(jsonResp)),
			}, nil
		},
	}

	// Override clientset creator to use our custom mock transport
	restore := k8cache.SetClientsetCreator(func(k *kubeconfig.Context, token string) (*kubernetes.Clientset, error) {
		restConf, err := k.RESTConfig()
		if err != nil {
			return nil, err
		}
		restConf.Transport = rt
		return kubernetes.NewForConfig(restConf)
	})
	defer restore()

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/clusters/kind-headlamp-admin/api/v1/pods", nil)
	r.Header.Set("Authorization", "Bearer abcdef")

	// Call IsAllowed twice
	allowed1, err := k8cache.IsAllowed("kind-headlamp-admin", mockK.Context, r)
	assert.NoError(t, err)
	assert.True(t, allowed1)

	allowed2, err := k8cache.IsAllowed("kind-headlamp-admin", mockK.Context, r)
	assert.NoError(t, err)
	assert.True(t, allowed2)

	// Assert that only 1 HTTP call was made because the second was served from cache
	assert.Equal(t, 1, calls)

	// Switch token, it should bypass the cache and make another call
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/clusters/kind-headlamp-admin/api/v1/pods", nil)
	r2.Header.Set("Authorization", "Bearer othertoken")

	allowed3, err := k8cache.IsAllowed("kind-headlamp-admin", mockK.Context, r2)
	assert.NoError(t, err)
	assert.True(t, allowed3)
	assert.Equal(t, 2, calls)
}

func TestPurgeSsarCacheOnContextCleanup(t *testing.T) {
	k8cache.ResetForTesting()

	mockK := MockKubeConfig{
		&kubeconfig.Context{
			ClusterID:   "/home/user/.kubeconfig+kind-headlamp-admin",
			Cluster:     &api.Cluster{Server: "https://example.com"},
			AuthInfo:    &api.AuthInfo{Token: "abcdef"},
			KubeContext: &api.Context{Cluster: "kind-headlamp-admin"},
		},
	}

	jsonResp := `{
		"apiVersion": "authorization.k8s.io/v1",
		"kind": "SelfSubjectAccessReview",
		"status": {
			"allowed": true
		}
	}`

	var calls int
	rt := &testRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: 201,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(jsonResp)),
			}, nil
		},
	}

	restore := k8cache.SetClientsetCreator(func(k *kubeconfig.Context, token string) (*kubernetes.Clientset, error) {
		restConf, err := k.RESTConfig()
		if err != nil {
			return nil, err
		}
		restConf.Transport = rt
		return kubernetes.NewForConfig(restConf)
	})
	defer restore()

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/clusters/kind-headlamp-admin/api/v1/pods", nil)
	r.Header.Set("Authorization", "Bearer abcdef")

	// Call IsAllowed to populate cache
	allowed, err := k8cache.IsAllowed("kind-headlamp-admin", mockK.Context, r)
	assert.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, 1, calls)

	// Clean up context, this should clear the clientset cache and the SSAR cache
	k8cache.ExportedCleanupRemovedContext(k8cache.New[string](), "kind-headlamp-admin")

	// Call IsAllowed again, it should have to re-evaluate (make a new HTTP call) since the cache was purged
	allowed2, err := k8cache.IsAllowed("kind-headlamp-admin", mockK.Context, r)
	assert.NoError(t, err)
	assert.True(t, allowed2)
	assert.Equal(t, 2, calls)
}
