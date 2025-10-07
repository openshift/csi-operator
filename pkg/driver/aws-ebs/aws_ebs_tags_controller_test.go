package aws_ebs

import (
	"testing"
	"time"

	//"github.com/aws/aws-sdk-go/aws"
	//"github.com/aws/aws-sdk-go/service/ec2"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	configv1 "github.com/openshift/api/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
)

// TestNewAndUpdatedTags checks that newAndUpdatedTags converts OpenShift AWS resource tags to AWS ec2.Tags correctly
func TestNewAndUpdatedTags(t *testing.T) {
	tests := []struct {
		name         string
		inputTags    []configv1.AWSResourceTag
		expectedTags []*ec2types.Tag
	}{
		{
			name: "Single tag",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
			},
			expectedTags: []*ec2types.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
			},
		},
		{
			name: "Multiple tags",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
				{Key: "key2", Value: "value2"},
			},
			expectedTags: []*ec2types.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
				{Key: aws.String("key2"), Value: aws.String("value2")},
			},
		},
		{
			name:         "Empty tags",
			inputTags:    []configv1.AWSResourceTag{},
			expectedTags: []*ec2types.Tag{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := newAndUpdatedTags(tt.inputTags)
			if len(result) != len(tt.expectedTags) {
				t.Fatalf("expected %d tags, got %d", len(tt.expectedTags), len(result))
			}

			for i, tag := range result {
				if *tag.Key != *tt.expectedTags[i].Key || *tag.Value != *tt.expectedTags[i].Value {
					t.Errorf("expected tag %v, got %v", tt.expectedTags[i], tag)
				}
			}
		})
	}
}

// TestVolumesIDsToResourceIDs checks that volumesIDsToResourceIDs converts a list of volume IDs to AWS resource IDs correctly
func TestVolumesIDsToResourceIDs(t *testing.T) {
	tests := []struct {
		name           string
		inputVolumeIDs []*v1.PersistentVolume
		expectedResult []*string
	}{
		{
			name: "pv-name",
			inputVolumeIDs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-1234",
							},
						},
					},
				},
			},
			expectedResult: []*string{aws.String("vol-1234")},
		},
		{
			name: "Multiple volume IDs",
			inputVolumeIDs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-1234",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV2",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-5678",
							},
						},
					},
				},
			},
			expectedResult: []*string{aws.String("vol-1234"), aws.String("vol-5678")},
		},
		{
			name:           "No volume IDs",
			inputVolumeIDs: []*v1.PersistentVolume{},
			expectedResult: []*string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pvsToResourceIDs(tt.inputVolumeIDs)
			if len(result) != len(tt.expectedResult) {
				t.Fatalf("expected %d resource IDs, got %d", len(tt.expectedResult), len(result))
			}

			for i, resourceID := range result {
				if resourceID != *tt.expectedResult[i] {
					t.Errorf("expected resource ID %s, got %s", *tt.expectedResult[i], resourceID)
				}
			}
		})
	}
}

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
			name: "multiple tags",
			tags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
				{Key: "key2", Value: "value2"},
			},
			expected: "716ad4d5f6009800d2c36703b5644368076ded2f87578a1803b572ab43d11ab7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := computeTagsHash(tt.tags)
			if hash != tt.expected {
				t.Errorf("computeTagsHash() = %v, want %v", hash, tt.expected)
			}
		})
	}
}

func TestGetAndSetPVTagHash(t *testing.T) {
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: make(map[string]string),
		},
	}

	hash := "test-hash"
	pv = setPVTagHash(pv, hash)

	retrievedHash := getPVTagHash(pv)
	if retrievedHash != hash {
		t.Errorf("getPVTagHash() = %v, want %v", retrievedHash, hash)
	}
}

func TestConvertPVsListToStringArray(t *testing.T) {
	tests := []struct {
		name     string
		pvs      []*v1.PersistentVolume
		expected []string
	}{
		{
			name:     "empty PVs",
			pvs:      []*v1.PersistentVolume{},
			expected: []string{},
		},
		{
			name: "single PV",
			pvs: []*v1.PersistentVolume{
				{ObjectMeta: metav1.ObjectMeta{Name: "pv1"}},
			},
			expected: []string{"pv1"},
		},
		{
			name: "multiple PVs",
			pvs: []*v1.PersistentVolume{
				{ObjectMeta: metav1.ObjectMeta{Name: "pv1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "pv2"}},
			},
			expected: []string{"pv1", "pv2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvNames := convertPVsListToStringArray(tt.pvs...)
			if len(pvNames) != len(tt.expected) {
				t.Fatalf("convertPVsListToStringArray() returned %d PV names, want %d", len(pvNames), len(tt.expected))
			}
			for i, pvName := range pvNames {
				if pvName != tt.expected[i] {
					t.Errorf("convertPVsListToStringArray() PV name %d = %v, want %v", i, pvName, tt.expected[i])
				}
			}
		})
	}
}

func TestFilterUpdatableVolumes(t *testing.T) {
	tests := []struct {
		name        string
		pvs         []*v1.PersistentVolume
		newTagsHash string
		expected    []*v1.PersistentVolume
	}{
		{
			name:        "no PVs",
			pvs:         []*v1.PersistentVolume{},
			newTagsHash: "new-hash",
			expected:    []*v1.PersistentVolume{},
		},
		{
			name: "PV with matching hash",
			pvs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv1",
						Annotations: map[string]string{
							tagHashAnnotationKey: "existing-hash",
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver: driverName,
							},
						},
					},
				},
			},
			newTagsHash: "existing-hash",
			expected:    []*v1.PersistentVolume{},
		},
		{
			name: "PV with non-matching hash",
			pvs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv1",
						Annotations: map[string]string{
							tagHashAnnotationKey: "old-hash",
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver: driverName,
							},
						},
					},
				},
			},
			newTagsHash: "new-hash",
			expected: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv1",
						Annotations: map[string]string{
							tagHashAnnotationKey: "old-hash",
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver: driverName,
							},
						},
					},
				},
			},
		},
		{
			name: "PV with no hash annotation",
			pvs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver: driverName,
							},
						},
					},
				},
			},
			newTagsHash: "new-hash",
			expected: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver: driverName,
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &EBSVolumeTagsController{}
			updatablePVs := c.filterUpdatableVolumes(tt.pvs, tt.newTagsHash)

			if len(updatablePVs) != len(tt.expected) {
				t.Fatalf("filterUpdatableVolumes() returned %d PVs, want %d", len(updatablePVs), len(tt.expected))
			}

			for i, pv := range updatablePVs {
				if pv.Name != tt.expected[i].Name {
					t.Errorf("filterUpdatableVolumes() PV %d = %v, want %v", i, pv.Name, tt.expected[i].Name)
				}
			}
		})
	}
}

func TestIsVolumeInQueue(t *testing.T) {
	c := &EBSVolumeTagsController{
		queueSet: make(map[string]struct{}),
	}

	// Add a volume to the queue
	c.addVolumesToQueueSet(&v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv1"}})

	tests := []struct {
		name       string
		volumeName string
		expected   bool
	}{
		{
			name:       "volume in queue",
			volumeName: "pv1",
			expected:   true,
		},
		{
			name:       "volume not in queue",
			volumeName: "pv2",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.isVolumeInQueue(tt.volumeName)
			if result != tt.expected {
				t.Errorf("isVolumeInQueue() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAddBatchVolumesToQueueWorker(t *testing.T) {
	c := &EBSVolumeTagsController{
		queue:    workqueue.NewTypedRateLimitingQueue[*pvUpdateQueueItem](workqueue.NewTypedItemExponentialFailureRateLimiter[*pvUpdateQueueItem](10*time.Second, 36*time.Hour)),
		queueSet: make(map[string]struct{}),
	}

	pvs := []*v1.PersistentVolume{
		{ObjectMeta: metav1.ObjectMeta{Name: "pv1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pv2"}},
	}

	c.addBatchVolumesToQueueWorker(pvs)

	// Verify that the PVs are added to the queue set
	for _, pv := range pvs {
		if !c.isVolumeInQueue(pv.Name) {
			t.Errorf("addBatchVolumesToQueueWorker() failed to add PV %s to queue set", pv.Name)
		}
	}

	item, _ := c.queue.Get()
	if item.updateType != updateTypeBatch {
		t.Errorf("addBatchVolumesToQueueWorker() updateType = %v, want %v", item.updateType, updateTypeBatch)
	}

	if len(item.pvNames) != len(pvs) {
		t.Errorf("addBatchVolumesToQueueWorker() added %d PV names, want %d", len(item.pvNames), len(pvs))
	}
}

func TestRemoveVolumesFromQueueSet(t *testing.T) {
	c := &EBSVolumeTagsController{
		queueSet: make(map[string]struct{}),
	}

	// Add volumes to the queue set
	c.addVolumesToQueueSet(&v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv1"}})
	c.addVolumesToQueueSet(&v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv2"}})

	// Remove a volume
	c.removeVolumesFromQueueSet("pv1")

	// Verify that the volume is removed
	if c.isVolumeInQueue("pv1") {
		t.Error("removeVolumesFromQueueSet() failed to remove PV pv1 from queue set")
	}

	// Verify that the other volume is still in the set
	if !c.isVolumeInQueue("pv2") {
		t.Error("removeVolumesFromQueueSet() incorrectly removed PV pv2 from queue set")
	}
}
