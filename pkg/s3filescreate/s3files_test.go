package s3filescreate

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3filestypes "github.com/aws/aws-sdk-go-v2/service/s3files/types"
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
