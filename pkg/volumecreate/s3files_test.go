package volumecreate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	s3filestypes "github.com/aws/aws-sdk-go-v2/service/s3files/types"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServiceTrustPolicyIsScopedToFileSystems(t *testing.T) {
	policy, err := json.Marshal(serviceTrustPolicy("123456789012", "us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	want := "arn:aws:s3files:us-east-1:123456789012:file-system/*"
	if !contains(string(policy), want) {
		t.Fatalf("trust policy does not contain %q: %s", want, policy)
	}
}

func TestDirectReadPolicyIsBucketScoped(t *testing.T) {
	policy, err := json.Marshal(directReadPolicy("test-bucket"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(policy), "arn:aws:s3:::test-bucket/*") {
		t.Fatalf("policy is not scoped to test-bucket: %s", policy)
	}
}

func TestBoundedName(t *testing.T) {
	if got := boundedName("abcdef", 4); got != "abcd" {
		t.Fatalf("got %q", got)
	}
}

func TestHasTag(t *testing.T) {
	tags := []s3filestypes.Tag{{Key: aws.String("kubernetes.io/cluster/test"), Value: aws.String("owned")}}
	if !hasTag(tags, "kubernetes.io/cluster/test", "owned") {
		t.Fatal("expected ownership tag to match")
	}
	if hasTag(tags, "kubernetes.io/cluster/test", "shared") {
		t.Fatal("unexpected ownership tag match")
	}
}

func TestInstanceProfileNamesIncludesEveryNodeRole(t *testing.T) {
	instance := func(id, profile string) ec2types.Instance {
		return ec2types.Instance{InstanceId: aws.String(id), IamInstanceProfile: &ec2types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::123456789012:instance-profile/" + profile)}}
	}
	// Control-plane and worker nodes use different instance profiles. Both must
	// be returned regardless of the order in which EC2 lists the instances.
	reservations := []ec2types.Reservation{
		{Instances: []ec2types.Instance{instance("i-master-0", "infra-master-profile"), instance("i-master-1", "infra-master-profile")}},
		{Instances: []ec2types.Instance{instance("i-worker-0", "infra-worker-profile")}},
		{Instances: []ec2types.Instance{instance("i-master-2", "infra-master-profile"), instance("i-worker-1", "infra-worker-profile")}},
	}
	got, err := instanceProfileNames(reservations)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"infra-master-profile", "infra-worker-profile"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("instanceProfileNames() = %v, want %v", got, want)
	}

	reservations = append(reservations, ec2types.Reservation{Instances: []ec2types.Instance{{InstanceId: aws.String("i-no-profile")}}})
	if _, err := instanceProfileNames(reservations); err == nil || !contains(err.Error(), "i-no-profile") {
		t.Fatalf("expected an error naming the instance without a profile, got %v", err)
	}
}

func TestValidateOperation(t *testing.T) {
	for _, operation := range []string{"create", "status", "delete"} {
		if err := ValidateOperation(operation, "", false); err != nil {
			t.Fatalf("validate %q: %v", operation, err)
		}
	}
	if err := ValidateOperation("describe", "", false); err == nil {
		t.Fatal("expected unsupported operation to fail")
	}
	if err := ValidateOperation("status", "us-east-1a", false); err == nil {
		t.Fatal("expected --single-zone with status to fail")
	}
	if err := ValidateOperation("create", "", true); err == nil {
		t.Fatal("expected --force with create to fail")
	}
}

func TestCleanupWorkloads(t *testing.T) {
	className := storageClassName
	workloads := cleanupWorkloads(
		[]corev1.PersistentVolume{
			{ObjectMeta: metav1.ObjectMeta{Name: "s3files-pv"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: efsCSIDriverName, VolumeHandle: "s3files:fs-target:/access-point"}}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "other-driver"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "other.csi.example", VolumeHandle: "s3files:fs-target"}}}},
			{ObjectMeta: metav1.ObjectMeta{Name: "other-filesystem"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: efsCSIDriverName, VolumeHandle: "s3files:fs-other"}}}},
		},
		[]corev1.PersistentVolumeClaim{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "s3files-pvc"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &className}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "other-pvc"}},
		},
		[]string{"fs-target"},
	)
	want := []string{"PersistentVolume/s3files-pv", "PersistentVolumeClaim/test/s3files-pvc"}
	if len(workloads) != len(want) {
		t.Fatalf("got %v, want %v", workloads, want)
	}
	for i := range want {
		if workloads[i] != want[i] {
			t.Fatalf("got %v, want %v", workloads, want)
		}
	}
}

func contains(value, substring string) bool {
	return len(value) >= len(substring) && stringContains(value, substring)
}

func stringContains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

type reviewHTTPClient func(*http.Request) (*http.Response, error)

func (f reviewHTTPClient) Do(req *http.Request) (*http.Response, error) { return f(req) }

type reviewResponse struct {
	operation string
	status    int
	body      string
	check     func(*http.Request)
}

// reviewConfig provides scripted SDK responses without contacting AWS.
func reviewConfig(t *testing.T, responses ...reviewResponse) aws.Config {
	t.Helper()
	next := 0
	t.Cleanup(func() {
		if next != len(responses) {
			t.Errorf("received %d requests, want %d", next, len(responses))
		}
	})
	return aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1,
		HTTPClient: reviewHTTPClient(func(req *http.Request) (*http.Response, error) {
			if next == len(responses) {
				t.Errorf("unexpected request: %s %s", req.Method, req.URL)
				return nil, fmt.Errorf("unexpected request")
			}
			response := responses[next]
			next++
			operation := req.Method + " " + req.URL.Path
			if strings.Contains(req.Header.Get("Content-Type"), "x-www-form-urlencoded") {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				values, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatal(err)
				}
				operation = values.Get("Action")
				req.Body = io.NopCloser(strings.NewReader(string(body)))
			}
			if operation != response.operation {
				t.Errorf("operation = %q, want %q", operation, response.operation)
			}
			if response.check != nil {
				response.check(req)
			}
			return &http.Response{StatusCode: response.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response.body)), Request: req}, nil
		}),
	}
}

// TestCreateRoleLookupErrors ensures lookup failures cannot trigger role creation.
func TestCreateRoleLookupErrors(t *testing.T) {
	for _, code := range []string{"AccessDenied", "Throttling"} {
		t.Run(code, func(t *testing.T) {
			cfg := reviewConfig(t, reviewResponse{operation: "GetRole", status: 403, body: "<ErrorResponse><Error><Code>" + code + "</Code><Message>lookup failed</Message></Error></ErrorResponse>"})
			c := &creator{ctx: context.Background(), iam: iam.NewFromConfig(cfg), role: "test-role"}
			_, err := c.createRole("123456789012")
			if err == nil || !strings.Contains(err.Error(), "get role test-role") {
				t.Fatalf("expected lookup error, got %v", err)
			}
		})
	}
}

// TestIAMPropagationErrors separates transient role visibility from permanent failures.
func TestIAMPropagationErrors(t *testing.T) {
	for _, test := range []struct {
		code, message string
		want          bool
	}{
		{"ValidationException", "IAM role is still propagating", true},
		{"ResourceNotFoundException", "IAM role not found", true},
		{"ValidationException", "IAM role is not yet available", true},
		{"AccessDeniedException", "Not authorized to pass role", false},
		{"ValidationException", "Role trust policy is invalid", false},
		{"ValidationException", "Unable to assume role", false},
		{"ResourceNotFoundException", "Bucket not found", false},
	} {
		t.Run(test.message, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &smithy.GenericAPIError{Code: test.code, Message: test.message})
			if got := isIAMPropagationError(err); got != test.want {
				t.Fatalf("isIAMPropagationError() = %v, want %v", got, test.want)
			}
		})
	}
}

// TestCreateFileSystemRetry verifies the SDK request is retried only for propagation.
func TestCreateFileSystemRetry(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprint(transient), func(t *testing.T) {
			message := "Role trust policy is invalid"
			if transient {
				message = "IAM role is still propagating"
			}
			responses := []reviewResponse{
				{operation: "GET /file-systems", status: 200, body: `{}`},
				{operation: "PUT /file-systems", status: 400, body: `{"__type":"ValidationException","message":"` + message + `"}`},
			}
			if transient {
				responses = append(responses,
					reviewResponse{operation: "PUT /file-systems", status: 201, body: `{"fileSystemId":"fs-test"}`},
					reviewResponse{operation: "GET /file-systems/fs-test", status: 200, body: `{"status":"available"}`})
			}
			cfg := reviewConfig(t, responses...)
			c := &creator{ctx: context.Background(), s3files: s3files.NewFromConfig(cfg), bucket: "test-bucket", infraName: "test"}
			id, err := c.createFileSystem("arn:aws:iam::123456789012:role/test")
			if transient && (err != nil || id != "fs-test") {
				t.Fatalf("createFileSystem() = %q, %v", id, err)
			}
			if !transient && (err == nil || !strings.Contains(err.Error(), message)) {
				t.Fatalf("expected permanent error, got %v", err)
			}
		})
	}
}

// TestSecurityGroupIngress verifies tagging and ingress repair on repeated creates.
func TestSecurityGroupIngress(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			groups := "<DescribeSecurityGroupsResponse><securityGroupInfo/></DescribeSecurityGroupsResponse>"
			if existing {
				groups = "<DescribeSecurityGroupsResponse><securityGroupInfo><item><groupId>sg-test</groupId></item></securityGroupInfo></DescribeSecurityGroupsResponse>"
			}
			responses := []reviewResponse{{operation: "DescribeSecurityGroups", status: 200, body: groups}}
			if !existing {
				responses = append(responses, reviewResponse{operation: "CreateSecurityGroup", status: 200, body: "<CreateSecurityGroupResponse><groupId>sg-test</groupId></CreateSecurityGroupResponse>", check: func(req *http.Request) {
					body, _ := io.ReadAll(req.Body)
					values, _ := url.ParseQuery(string(body))
					if values.Get("TagSpecification.1.ResourceType") != "security-group" || values.Get("TagSpecification.1.Tag.1.Key") != "Name" || values.Get("TagSpecification.1.Tag.2.Key") != "kubernetes.io/cluster/test" {
						t.Errorf("missing creation tags: %s", body)
					}
				}})
			}
			responses = append(responses, reviewResponse{operation: "AuthorizeSecurityGroupIngress", status: 400, body: "<Response><Errors><Error><Code>InvalidPermission.Duplicate</Code><Message>exists</Message></Error></Errors></Response>", check: func(req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				values, _ := url.ParseQuery(string(body))
				if values.Get("GroupId") != "sg-test" || values.Get("IpPermissions.1.FromPort") != "2049" || values.Get("IpPermissions.1.IpRanges.1.CidrIp") != "10.0.0.0/16" {
					t.Errorf("incorrect ingress: %s", body)
				}
			}})
			cfg := reviewConfig(t, responses...)
			c := &creator{ctx: context.Background(), ec2: ec2.NewFromConfig(cfg), group: "test-sg", infraName: "test"}
			if id, err := c.createSecurityGroup("vpc-test", "10.0.0.0/16"); err != nil || id != "sg-test" {
				t.Fatalf("createSecurityGroup() = %q, %v", id, err)
			}
		})
	}
}

// TestDeleteBucketObjectErrors ensures partial deletion failures stop bucket deletion.
func TestDeleteBucketObjectErrors(t *testing.T) {
	cfg := reviewConfig(t,
		reviewResponse{operation: "GET /", status: 200, body: `<ListVersionsResult><Version><Key>locked</Key><VersionId>v1</VersionId></Version></ListVersionsResult>`},
		reviewResponse{operation: "POST /", status: 200, body: `<DeleteResult><Error><Key>locked</Key><VersionId>v1</VersionId><Code>AccessDenied</Code><Message>Object is locked</Message></Error></DeleteResult>`},
	)
	c := &creator{ctx: context.Background(), s3: s3.NewFromConfig(cfg), bucket: "test-bucket"}
	err := c.deleteBucket()
	for _, want := range []string{"test-bucket", "locked", "v1", "AccessDenied", "Object is locked"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error containing %q, got %v", want, err)
		}
	}
}

// TestWaitCancellation ensures cancellation interrupts even a long polling delay.
func TestWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitForContext(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait() = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not stop on cancellation")
	}
}
