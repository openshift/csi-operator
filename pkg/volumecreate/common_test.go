package volumecreate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestNewGenericCreatorAWSInfrastructure(t *testing.T) {
	setLocalAWSEnvironment(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/apis/config.openshift.io/v1/infrastructures/cluster" {
			t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"config.openshift.io/v1","kind":"Infrastructure","metadata":{"name":"cluster"},"status":{"infrastructureName":"test-infra","platformStatus":{"type":"AWS","aws":{"region":"us-east-1"}}}}`))
	}))
	defer server.Close()

	creator, err := newGenericCreator(context.Background(), &rest.Config{Host: server.URL}, true)
	if err != nil {
		t.Fatal(err)
	}
	if creator.kube == nil || creator.ec2 == nil {
		t.Fatal("newGenericCreator() did not initialize shared clients")
	}
	if creator.infraName != "test-infra" || creator.region != "us-east-1" {
		t.Fatalf("newGenericCreator() infrastructure = (%q, %q), want (%q, %q)", creator.infraName, creator.region, "test-infra", "us-east-1")
	}
	credentials, err := creator.awsConfig.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve configured AWS credentials: %v", err)
	}
	if credentials.AccessKeyID != "test-access-key" {
		t.Fatalf("AWS access key = %q, want local environment credentials", credentials.AccessKeyID)
	}
	if requests != 1 {
		t.Fatalf("Kubernetes requests = %d, want only the infrastructure request", requests)
	}
}

func TestNewGenericCreatorRejectsNonAWSInfrastructure(t *testing.T) {
	server := infrastructureServer(t, `{"apiVersion":"config.openshift.io/v1","kind":"Infrastructure","metadata":{"name":"cluster"},"status":{"platformStatus":{"type":"Azure"}}}`)
	defer server.Close()

	_, err := newGenericCreator(context.Background(), &rest.Config{Host: server.URL}, true)
	if err == nil || err.Error() != "cluster is not running on AWS" {
		t.Fatalf("newGenericCreator() error = %v, want non-AWS error", err)
	}
}

func TestNewGenericCreatorPropagatesCancellation(t *testing.T) {
	server := infrastructureServer(t, `{"apiVersion":"config.openshift.io/v1","kind":"Infrastructure","metadata":{"name":"cluster"}}`)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newGenericCreator(ctx, &rest.Config{Host: server.URL}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("newGenericCreator() error = %v, want context.Canceled", err)
	}
}

func infrastructureServer(t *testing.T, infrastructure string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/apis/config.openshift.io/v1/infrastructures/cluster" {
			t.Errorf("unexpected Kubernetes request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(infrastructure))
	}))
}

func setLocalAWSEnvironment(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(configDir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(configDir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}
