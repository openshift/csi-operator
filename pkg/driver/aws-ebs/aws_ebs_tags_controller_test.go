package aws_ebs

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	configv1 "github.com/openshift/api/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

// TestNewAndUpdatedTags checks that newAndUpdatedTags converts OpenShift AWS resource tags to AWS ec2.Tags correctly
func TestNewAndUpdatedTags(t *testing.T) {
	tests := []struct {
		name         string
		inputTags    []configv1.AWSResourceTag
		expectedTags []*ec2.Tag
	}{
		{
			name: "Single tag",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
			},
			expectedTags: []*ec2.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
			},
		},
		{
			name: "Multiple tags",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
				{Key: "key2", Value: "value2"},
			},
			expectedTags: []*ec2.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
				{Key: aws.String("key2"), Value: aws.String("value2")},
			},
		},
		{
			name:         "Empty tags",
			inputTags:    []configv1.AWSResourceTag{},
			expectedTags: []*ec2.Tag{},
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
				if *resourceID != *tt.expectedResult[i] {
					t.Errorf("expected resource ID %s, got %s", *tt.expectedResult[i], *resourceID)
				}
			}
		})
	}
}
