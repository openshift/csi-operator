package aws_efs

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/efs"
	configv1 "github.com/openshift/api/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewAndUpdatedTags(t *testing.T) {
	resourceTags := []configv1.AWSResourceTag{
		{Key: "env", Value: "prod"},
		{Key: "app", Value: "myapp"},
	}

	expectedTags := []*efs.Tag{
		{Key: aws.String("env"), Value: aws.String("prod")},
		{Key: aws.String("app"), Value: aws.String("myapp")},
	}

	tags := newAndUpdatedTags(resourceTags)
	if !reflect.DeepEqual(tags, expectedTags) {
		t.Errorf("Expected %v, but got %v", expectedTags, tags)
	}
}

func TestComputeTagsHash(t *testing.T) {
	resourceTags := []configv1.AWSResourceTag{
		{Key: "env", Value: "prod"},
		{Key: "app", Value: "myapp"},
	}

	// Expected hash is deterministic and sorted
	concatenated := "app=myapp;env=prod;"
	hash := sha256.Sum256([]byte(concatenated))
	expectedHash := hex.EncodeToString(hash[:])

	computedHash := computeTagsHash(resourceTags)
	if computedHash != expectedHash {
		t.Errorf("Expected hash %v, but got %v", expectedHash, computedHash)
	}
}

func TestParseAccessPointID(t *testing.T) {
	volumeHandle := "fs-0123456789abcdef::fsap-12345678"
	expectedID := "fsap-12345678"
	accessPointID := parseAccessPointID(volumeHandle)
	if accessPointID != expectedID {
		t.Errorf("Expected access point ID %v, but got %v", expectedID, accessPointID)
	}
}

func TestSetPVTagHash(t *testing.T) {
	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}

	newHash := "newHash"
	updatedPV := setPVTagHash(pv, newHash)

	if updatedPV.Annotations[tagHashAnnotationKey] != newHash {
		t.Errorf("Expected annotation %v, but got %v", newHash, updatedPV.Annotations[tagHashAnnotationKey])
	}
}
