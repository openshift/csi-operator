package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestCommandHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root",
			args: []string{"--help"},
			want: []string{"volume-create", "efs", "s3", "--local-aws-creds"},
		},
		{
			name: "efs create",
			args: []string{"efs", "create", "--help"},
			want: []string{"--kubeconfig", "--single-zone", "--local-aws-creds"},
		},
		{
			name: "efs describe",
			args: []string{"efs", "describe", "--help"},
			want: []string{"--kubeconfig", "--local-aws-creds"},
		},
		{
			name: "s3 create",
			args: []string{"s3", "create", "--help"},
			want: []string{"--kubeconfig", "--single-zone", "--local-aws-creds"},
		},
		{
			name: "s3 delete",
			args: []string{"s3", "delete", "--help"},
			want: []string{"--kubeconfig", "--force", "--local-aws-creds"},
		},
		{
			name: "s3 describe",
			args: []string{"s3", "describe", "--help"},
			want: []string{"--kubeconfig", "--local-aws-creds"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := newCommand()
			output := &bytes.Buffer{}
			cmd.SetOut(output)
			cmd.SetErr(output)
			cmd.SetArgs(test.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("help output does not contain %q:\n%s", want, output.String())
				}
			}
			for _, unwanted := range []string{"--config", "--listen", "--namespace", "--terminate-on-files"} {
				if strings.Contains(output.String(), unwanted) {
					t.Errorf("help output contains unsupported flag %q:\n%s", unwanted, output.String())
				}
			}
		})
	}
}

func TestOperationFlagsAreLimitedToTheirCommands(t *testing.T) {
	tests := []struct {
		args []string
	}{
		{args: []string{"s3", "delete", "--single-zone", "us-east-1a"}},
		{args: []string{"s3", "describe", "--single-zone", "us-east-1a"}},
		{args: []string{"s3", "describe", "--force"}},
		{args: []string{"efs", "create", "--config", "config.yaml"}},
		{args: []string{"efs", "create", "--listen", "127.0.0.1:8080"}},
		{args: []string{"efs", "create", "--namespace", "default"}},
		{args: []string{"efs", "create", "--terminate-on-files", "file"}},
		{args: []string{"efs", "describe", "--single-zone", "us-east-1a"}},
	}

	for _, test := range tests {
		cmd := newCommand()
		cmd.SetArgs(test.args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%q succeeded with an unsupported flag", test.args)
		}
	}
}

type recordingBackend struct {
	ctx        context.Context
	local      bool
	singleZone string
	force      bool
	operation  string
}

func (b *recordingBackend) run(ctx context.Context, _ *rest.Config, local bool, singleZone string, force bool, operation string) error {
	b.ctx, b.local, b.singleZone, b.force, b.operation = ctx, local, singleZone, force, operation
	return ctx.Err()
}

func TestOperationCommandDispatchesFlagsAndContext(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(configPath, []byte("apiVersion: v1\nclusters:\n- cluster:\n    server: https://example.invalid\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &recordingBackend{}
	localCredentials := false
	cmd := newOperationCommand(operationOptions{
		operation:             "delete",
		description:           "test",
		singleZoneDescription: "zone",
		forceDescription:      "force",
		backend:               backend,
	}, &localCredentials)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--kubeconfig", configPath, "--single-zone", "us-east-1a", "--force"})
	if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() = %v, want context.Canceled", err)
	}
	if backend.ctx != ctx || backend.operation != "delete" || backend.singleZone != "us-east-1a" || !backend.force || backend.local {
		t.Fatalf("backend received ctx=%v operation=%q zone=%q force=%v local=%v", backend.ctx, backend.operation, backend.singleZone, backend.force, backend.local)
	}
}
