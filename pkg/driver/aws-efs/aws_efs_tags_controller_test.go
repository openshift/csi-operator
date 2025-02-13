package aws_efs

import (
	"context"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	configv1 "github.com/openshift/api/config/v1"
)

func TestComputeTagsHash(t *testing.T) {
	tests := []struct {
		name     string
		tags     []configv1.AWSResourceTag
		expected string
	}{
		{
			name:     "empty tags",
			tags:     []configv1.AWSResourceTag{},
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "single tag",
			tags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
			},
			expected: "360222661b6e9726460f6456f2b1dc88ef12447ec526cf9b64dfdeb0ef5631a1",
		},
		{
			name: "multiple tags sorted",
			tags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
				{Key: "key2", Value: "value2"},
			},
			expected: "716ad4d5f6009800d2c36703b5644368076ded2f87578a1803b572ab43d11ab7",
		},
		{
			name: "same tags different order",
			tags: []configv1.AWSResourceTag{
				{Key: "key2", Value: "value2"},
				{Key: "key1", Value: "value1"},
			},
			expected: "716ad4d5f6009800d2c36703b5644368076ded2f87578a1803b572ab43d11ab7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeTagsHash(tt.tags); got != tt.expected {
				t.Errorf("computeTagsHash() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseAccessPointID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid access point",
			input:    "fs-12345678::fsap-12345678901234567",
			expected: "fsap-12345678901234567",
		},
		{
			name:     "no access point",
			input:    "fs-12345678",
			expected: "",
		},
		{
			name:     "invalid format",
			input:    "invalid::format::multiple",
			expected: "",
		},
		{
			name:     "non-access-point suffix",
			input:    "fs-12345678::not-an-access-point",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAccessPointID(tt.input); got != tt.expected {
				t.Errorf("parseAccessPointID() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFilterUpdatableVolumes(t *testing.T) {
	newHash := computeTagsHash([]configv1.AWSResourceTag{
		{Key: "cluster", Value: "test"},
	})

	testPVs := []*v1.PersistentVolume{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv1"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       efsDriverName,
						VolumeHandle: "fs-123::fsap-123",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "pv2",
				Annotations: map[string]string{tagHashAnnotationKey: newHash},
			},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       efsDriverName,
						VolumeHandle: "fs-123::fsap-456",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv3"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "different.driver",
						VolumeHandle: "fs-123::fsap-789",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv4"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					NFS: &v1.NFSVolumeSource{},
				},
			},
		},
	}

	c := &EFSAccessPointTagsController{}
	filtered := c.filterUpdatableVolumes(testPVs, newHash)

	if len(filtered) != 1 {
		t.Fatalf("Expected 1 updatable volume, got %d", len(filtered))
	}

	if filtered[0].Name != "pv1" {
		t.Errorf("Expected pv1 to be filtered, got %s", filtered[0].Name)
	}
}

func TestQueueOperations(t *testing.T) {
	c := &EFSAccessPointTagsController{
		queueSet: make(map[string]struct{}),
		queue:    workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Second, 100*time.Hour)),
	}

	volumeName := "test-volume"

	// Test adding to queue
	c.addToQueue(volumeName)
	if !c.isVolumeInQueue(volumeName) {
		t.Error("Volume should be in queue after add")
	}

	// Test removing from queue
	c.removeFromQueue(volumeName)
	if c.isVolumeInQueue(volumeName) {
		t.Error("Volume should not be in queue after removal")
	}
}

func TestAnnotationHelpers(t *testing.T) {
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: make(map[string]string),
		},
	}

	// Test setting hash
	testHash := "test-hash-123"
	updatedPV := setPVTagHash(pv, testHash)
	if updatedPV.Annotations[tagHashAnnotationKey] != testHash {
		t.Errorf("Annotation not set correctly, got %s", updatedPV.Annotations[tagHashAnnotationKey])
	}

	// Test getting hash
	if got := getPVTagHash(updatedPV); got != testHash {
		t.Errorf("getPVTagHash() = %v, want %v", got, testHash)
	}

	// Test empty hash
	if got := getPVTagHash(&v1.PersistentVolume{}); got != "" {
		t.Errorf("Expected empty hash, got %v", got)
	}
}

func TestQueueConcurrency(t *testing.T) {
	c := &EFSAccessPointTagsController{
		queueSet: make(map[string]struct{}),
		queue:    workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Second, 100*time.Hour)),
	}

	volumeNames := []string{"vol1", "vol2", "vol3"}

	var wg sync.WaitGroup
	wg.Add(len(volumeNames))

	// Concurrent adds
	for _, name := range volumeNames {
		go func(n string) {
			defer wg.Done()
			c.addToQueue(n)
		}(name)
	}
	wg.Wait()

	// Verify all volumes were added
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if len(c.queueSet) != len(volumeNames) {
		t.Errorf("Expected %d volumes in queue, got %d", len(volumeNames), len(c.queueSet))
	}
}

func TestGetPVTagHash_MissingAnnotations(t *testing.T) {
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			// No annotations
		},
	}

	if hash := getPVTagHash(pv); hash != "" {
		t.Errorf("Expected empty hash, got %q", hash)
	}
}

func TestProcessInfrastructure_NoAWSTags(t *testing.T) {
	c := &EFSAccessPointTagsController{}
	infra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: nil,
				},
			},
		},
	}

	err := c.processInfrastructure(context.Background(), infra)
	if err != nil {
		t.Errorf("Unexpected error processing infrastructure: %v", err)
	}
}

func TestFilterUpdatableVolumes_SameHash(t *testing.T) {
	commonTags := []configv1.AWSResourceTag{
		{Key: "cluster", Value: "test"},
	}
	currentHash := computeTagsHash(commonTags)

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-pv",
			Annotations: map[string]string{tagHashAnnotationKey: currentHash},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       efsDriverName,
					VolumeHandle: "fs-123::fsap-123",
				},
			},
		},
	}

	c := &EFSAccessPointTagsController{}
	filtered := c.filterUpdatableVolumes([]*v1.PersistentVolume{pv}, currentHash)

	if len(filtered) != 0 {
		t.Errorf("Expected no updatable volumes, got %d", len(filtered))
	}
}

func TestNeedsTagUpdate(t *testing.T) {
	controller := &EFSAccessPointTagsController{}

	infra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					ResourceTags: []configv1.AWSResourceTag{
						{Key: "env", Value: "prod"},
					},
				},
			},
		},
	}

	pv := &v1.PersistentVolume{}

	// Test case where no existing hash is present
	if !controller.needsTagUpdate(infra, pv) {
		t.Errorf("Expected needsTagUpdate to return true, got false")
	}

	// Simulating existing hash matching new hash
	pv = setPVTagHash(pv, computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags))
	if controller.needsTagUpdate(infra, pv) {
		t.Errorf("Expected needsTagUpdate to return false, got true")
	}
}

func TestIsHypershiftCluster(t *testing.T) {
	// Positive test case: Label "hypershift.openshift.io/managed" set to "true" should return true
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"hypershift.openshift.io/managed": "true",
			},
		},
	}
	if !isHypershiftCluster(infra) {
		t.Errorf("Expected isHypershiftCluster to return true, got false")
	}

	// Negative test case: Label not set should return false
	infra = &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{},
		},
	}
	if isHypershiftCluster(infra) {
		t.Errorf("Expected isHypershiftCluster to return false, got true")
	}

	// Negative test case: Label set to "false" should return false
	infra = &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"hypershift.openshift.io/managed": "false",
			},
		},
	}
	if isHypershiftCluster(infra) {
		t.Errorf("Expected isHypershiftCluster to return false, got true")
	}

	// Edge case: nil Labels map should return false
	infra = &configv1.Infrastructure{}
	if isHypershiftCluster(infra) {
		t.Errorf("Expected isHypershiftCluster to return false, got true")
	}
}
