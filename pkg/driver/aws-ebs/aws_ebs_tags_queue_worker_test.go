package aws_ebs

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/operator/events"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecore "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
)

// mockEC2Client implements ec2TagsAPI for unit testing.
type mockEC2Client struct {
	createTagsFunc   func(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	describeTagsFunc func(ctx context.Context, params *ec2.DescribeTagsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error)
}

func (m *mockEC2Client) CreateTags(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	if m.createTagsFunc != nil {
		return m.createTagsFunc(ctx, params, optFns...)
	}
	return &ec2.CreateTagsOutput{}, nil
}

func (m *mockEC2Client) DescribeTags(ctx context.Context, params *ec2.DescribeTagsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
	if m.describeTagsFunc != nil {
		return m.describeTagsFunc(ctx, params, optFns...)
	}
	return &ec2.DescribeTagsOutput{}, nil
}

func newTestController() *EBSVolumeTagsController {
	return &EBSVolumeTagsController{
		queue:         workqueue.NewTypedRateLimitingQueue[*pvUpdateQueueItem](workqueue.NewTypedItemExponentialFailureRateLimiter[*pvUpdateQueueItem](1*time.Millisecond, 1*time.Millisecond)),
		queueSet:      make(map[string]struct{}),
		eventRecorder: events.NewInMemoryRecorder("test", &clock.RealClock{}),
	}
}

func newTestControllerWithFakeKubeClient(t *testing.T, pvs ...*v1.PersistentVolume) *EBSVolumeTagsController {
	t.Helper()
	cr := clients.GetFakeOperatorCR()
	c := clients.NewFakeClients("openshift-cluster-csi-drivers", cr)

	// Access the PV informer before starting informers so it gets registered.
	// Then add PVs directly to the informer store, which is the most reliable
	// way to populate fake informer caches (same pattern as aws_ebs_test.go).
	pvInformer := c.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Informer()
	clients.SyncFakeInformers(t, c)

	for _, pv := range pvs {
		if err := pvInformer.GetStore().Add(pv); err != nil {
			t.Fatalf("failed to add PV %s to informer store: %v", pv.Name, err)
		}
		// Also add to the fake client so that Updates (e.g. annotation writes) work.
		if err := c.KubeClient.(*fakecore.Clientset).Tracker().Add(pv); err != nil {
			t.Fatalf("failed to add PV %s to tracker: %v", pv.Name, err)
		}
	}

	return &EBSVolumeTagsController{
		commonClient:  c,
		queue:         workqueue.NewTypedRateLimitingQueue[*pvUpdateQueueItem](workqueue.NewTypedItemExponentialFailureRateLimiter[*pvUpdateQueueItem](1*time.Millisecond, 1*time.Millisecond)),
		queueSet:      make(map[string]struct{}),
		eventRecorder: events.NewInMemoryRecorder("test", &clock.RealClock{}),
	}
}

func newTestPV(name, volumeHandle string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       driverName,
					VolumeHandle: volumeHandle,
				},
			},
		},
	}
}

func newTestInfra(tags []configv1.AWSResourceTag) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					Region:       "us-east-1",
					ResourceTags: tags,
				},
			},
		},
	}
}

func TestHandleBatchTagUpdateFailure(t *testing.T) {
	tests := []struct {
		name          string
		pvs           []*v1.PersistentVolume
		wantQueueSize int
	}{
		{
			name: "small batch re-queued individually",
			pvs: []*v1.PersistentVolume{
				newTestPV("pv1", "vol-111"),
				newTestPV("pv2", "vol-222"),
				newTestPV("pv3", "vol-333"),
			},
			wantQueueSize: 3,
		},
		{
			name: "large batch re-queued individually",
			pvs: []*v1.PersistentVolume{
				newTestPV("pv1", "vol-111"),
				newTestPV("pv2", "vol-222"),
				newTestPV("pv3", "vol-333"),
				newTestPV("pv4", "vol-444"),
				newTestPV("pv5", "vol-555"),
			},
			wantQueueSize: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestController()
			c.handleBatchTagUpdateFailure(tt.pvs, fmt.Errorf("batch failed"))

			for i := 0; i < tt.wantQueueSize; i++ {
				item, quit := c.queue.Get()
				if quit {
					t.Fatalf("queue shut down unexpectedly after %d items", i)
				}
				if item.updateType != updateTypeIndividual {
					t.Errorf("item %d: updateType = %v, want %v", i, item.updateType, updateTypeIndividual)
				}
				if len(item.pvNames) != 1 {
					t.Errorf("item %d: got %d pvNames, want 1", i, len(item.pvNames))
				}
				if item.pvNames[0] != tt.pvs[i].Name {
					t.Errorf("item %d: pvName = %s, want %s", i, item.pvNames[0], tt.pvs[i].Name)
				}
				c.queue.Done(item)
			}

			recorder := c.eventRecorder.(events.InMemoryRecorder)
			foundWarning := false
			for _, event := range recorder.Events() {
				if event.Reason == "EBSVolumeTagsUpdateFailed" {
					foundWarning = true
					break
				}
			}
			if !foundWarning {
				t.Error("expected EBSVolumeTagsUpdateFailed event, not found")
			}
		})
	}
}

func TestNeedsTagUpdate(t *testing.T) {
	resourceTags := []configv1.AWSResourceTag{{Key: "key1", Value: "value1"}}
	infra := newTestInfra(resourceTags)
	expectedHash := computeTagsHash(resourceTags)

	tests := []struct {
		name     string
		pv       *v1.PersistentVolume
		expected bool
	}{
		{
			name:     "no hash annotation - needs update",
			pv:       newTestPV("pv1", "vol-111"),
			expected: true,
		},
		{
			name: "matching hash - no update needed",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "pv2",
					Annotations: map[string]string{tagHashAnnotationKey: expectedHash},
				},
			},
			expected: false,
		},
		{
			name: "stale hash - needs update",
			pv: &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "pv3",
					Annotations: map[string]string{tagHashAnnotationKey: "old-hash"},
				},
			},
			expected: true,
		},
	}

	c := newTestController()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.needsTagUpdate(infra, tt.pv)
			if result != tt.expected {
				t.Errorf("needsTagUpdate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestProcessBatchVolumes(t *testing.T) {
	resourceTags := []configv1.AWSResourceTag{{Key: "env", Value: "prod"}}
	expectedHash := computeTagsHash(resourceTags)

	// Each test case provides a setup func that builds the mock and returns a
	// verify func. This lets the mock and verify share state (e.g. captured
	// call arguments) via a closure without awkward struct field tricks.
	tests := []struct {
		name    string
		pvs     []*v1.PersistentVolume
		pvNames []string
		setup   func(t *testing.T) (mock *mockEC2Client, verify func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem))
	}{
		{
			name:    "success: tags applied and annotations updated",
			pvs:     []*v1.PersistentVolume{newTestPV("pv1", "vol-111"), newTestPV("pv2", "vol-222")},
			pvNames: []string{"pv1", "pv2"},
			setup: func(t *testing.T) (*mockEC2Client, func(*testing.T, *EBSVolumeTagsController, *pvUpdateQueueItem)) {
				mock := &mockEC2Client{
					describeTagsFunc: func(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
						return &ec2.DescribeTagsOutput{}, nil
					},
				}
				verify := func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem) {
					for _, name := range []string{"pv1", "pv2"} {
						if c.isVolumeInQueue(name) {
							t.Errorf("%s should have been removed from queueSet after successful update", name)
						}
						updated, err := c.commonClient.KubeClient.CoreV1().PersistentVolumes().Get(context.Background(), name, metav1.GetOptions{})
						if err != nil {
							t.Fatalf("failed to get %s: %v", name, err)
						}
						if updated.Annotations[tagHashAnnotationKey] != expectedHash {
							t.Errorf("%s hash = %q, want %q", name, updated.Annotations[tagHashAnnotationKey], expectedHash)
						}
					}
				}
				return mock, verify
			},
		},
		{
			name:    "success: all volumes already tagged, CreateTags not called",
			pvs:     []*v1.PersistentVolume{newTestPV("pv1", "vol-111"), newTestPV("pv2", "vol-222")},
			pvNames: []string{"pv1", "pv2"},
			setup: func(t *testing.T) (*mockEC2Client, func(*testing.T, *EBSVolumeTagsController, *pvUpdateQueueItem)) {
				mock := &mockEC2Client{
					describeTagsFunc: func(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
						return &ec2.DescribeTagsOutput{
							Tags: []ec2types.TagDescription{
								{ResourceId: aws.String("vol-111"), Key: aws.String("env"), Value: aws.String("prod"), ResourceType: ec2types.ResourceTypeVolume},
								{ResourceId: aws.String("vol-222"), Key: aws.String("env"), Value: aws.String("prod"), ResourceType: ec2types.ResourceTypeVolume},
							},
						}, nil
					},
					createTagsFunc: func(_ context.Context, _ *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
						t.Error("CreateTags should NOT have been called when all volumes already have tags")
						return &ec2.CreateTagsOutput{}, nil
					},
				}
				verify := func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem) {
					for _, name := range []string{"pv1", "pv2"} {
						if c.isVolumeInQueue(name) {
							t.Errorf("%s should have been removed from queueSet", name)
						}
					}
				}
				return mock, verify
			},
		},
		{
			name:    "success: only untagged volumes sent to CreateTags",
			pvs:     []*v1.PersistentVolume{newTestPV("pv1", "vol-111"), newTestPV("pv2", "vol-222"), newTestPV("pv3", "vol-333")},
			pvNames: []string{"pv1", "pv2", "pv3"},
			setup: func(t *testing.T) (*mockEC2Client, func(*testing.T, *EBSVolumeTagsController, *pvUpdateQueueItem)) {
				var taggedVolumeIDs []string
				mock := &mockEC2Client{
					describeTagsFunc: func(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
						// vol-222 is already tagged; vol-111 and vol-333 need tagging.
						return &ec2.DescribeTagsOutput{
							Tags: []ec2types.TagDescription{
								{ResourceId: aws.String("vol-222"), Key: aws.String("env"), Value: aws.String("prod"), ResourceType: ec2types.ResourceTypeVolume},
							},
						}, nil
					},
					createTagsFunc: func(_ context.Context, params *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
						taggedVolumeIDs = params.Resources
						return &ec2.CreateTagsOutput{}, nil
					},
				}
				verify := func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem) {
					if len(taggedVolumeIDs) != 2 {
						t.Fatalf("expected CreateTags for 2 volumes, got %d: %v", len(taggedVolumeIDs), taggedVolumeIDs)
					}
					for _, id := range taggedVolumeIDs {
						if id == "vol-222" {
							t.Error("vol-222 should have been skipped (already tagged)")
						}
					}
					for _, name := range []string{"pv1", "pv2", "pv3"} {
						if c.isVolumeInQueue(name) {
							t.Errorf("%s should have been removed from queueSet", name)
						}
					}
				}
				return mock, verify
			},
		},
		{
			name:    "error: DescribeTags failure re-queues whole batch",
			pvs:     []*v1.PersistentVolume{newTestPV("pv1", "vol-111"), newTestPV("pv2", "vol-222"), newTestPV("pv3", "vol-333")},
			pvNames: []string{"pv1", "pv2", "pv3"},
			setup: func(t *testing.T) (*mockEC2Client, func(*testing.T, *EBSVolumeTagsController, *pvUpdateQueueItem)) {
				mock := &mockEC2Client{
					describeTagsFunc: func(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
						return nil, fmt.Errorf("DescribeTags throttled")
					},
				}
				verify := func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem) {
					// forget the item here to avoid rate limiting
					c.queue.Forget(workItem)
					var updateItem *pvUpdateQueueItem

					wg := sync.WaitGroup{}
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							item, _ := c.queue.Get()
							updateItem = item
							break
						}
					}()
					wg.Wait()

					expectedPVNames := []string{"pv1", "pv2", "pv3"}
					if !reflect.DeepEqual(updateItem.pvNames, expectedPVNames) {
						t.Errorf("expected %+v, got %+v", expectedPVNames, updateItem.pvNames)
					}
					if updateItem.updateType != updateTypeBatch {
						t.Errorf("expected batched item, got %+v", updateItem.updateType)
					}
					for _, pvName := range expectedPVNames {
						if !c.isVolumeInQueue(pvName) {
							t.Errorf("%s should have been added to queueSet", pvName)
						}
					}
				}
				return mock, verify
			},
		},
		{
			name:    "error: CreateTags failure re-queues each volume individually",
			pvs:     []*v1.PersistentVolume{newTestPV("pv1", "vol-111"), newTestPV("pv2", "vol-222")},
			pvNames: []string{"pv1", "pv2"},
			setup: func(t *testing.T) (*mockEC2Client, func(*testing.T, *EBSVolumeTagsController, *pvUpdateQueueItem)) {
				mock := &mockEC2Client{
					describeTagsFunc: func(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
						return &ec2.DescribeTagsOutput{}, nil
					},
					createTagsFunc: func(_ context.Context, _ *ec2.CreateTagsInput, _ ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
						return nil, fmt.Errorf("InvalidVolume.NotFound: vol-222 does not exist")
					},
				}
				verify := func(t *testing.T, c *EBSVolumeTagsController, workItem *pvUpdateQueueItem) {
					c.queue.Forget(workItem)
					workItems := []*pvUpdateQueueItem{}
					wg := sync.WaitGroup{}
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							item, quit := c.queue.Get()
							if item != nil {
								workItems = append(workItems, item)
							}
							if len(workItems) == 2 || quit {
								break
							}
						}
					}()
					wg.Wait()
					if len(workItems) != 2 {
						t.Errorf("Expected 2 work items, got %d", len(workItems))
					}
					for _, item := range workItems {
						if item.updateType != updateTypeIndividual {
							t.Errorf("Expected updateTypeIndividual, got %v", item.updateType)
						}
						c.queue.Done(item)
					}
					expectedPVNames := []string{"pv1", "pv2"}
					for _, pvName := range expectedPVNames {
						if !c.isVolumeInQueue(pvName) {
							t.Errorf("%s should have been added to queueSet", pvName)
						}
					}
				}
				return mock, verify
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, verify := tt.setup(t)

			c := newTestControllerWithFakeKubeClient(t, tt.pvs...)
			c.addVolumesToQueueSet(tt.pvs...)

			infra := newTestInfra(resourceTags)
			item := &pvUpdateQueueItem{updateType: updateTypeBatch, pvNames: tt.pvNames}
			c.queue.Add(item)
			c.queue.Get() // mark as processing

			c.processBatchVolumes(t.Context(), item, infra, mock)
			// Mark the original item done so any rate-limited re-adds become dequeue-able.
			c.queue.Done(item)

			verify(t, c, item)
		})
	}
}
