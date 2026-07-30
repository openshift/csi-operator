package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

var (
	numIterations  = 10
	delayIteration = 5 * time.Second
)

func applyPrerequisites(
	ctx context.Context,
	kubeClient kubeclient.Interface,
	recorder events.Recorder,
	assetNames []string,
	assetFunc resourceapply.AssetFunc,
) error {

	var errs []error
	for range numIterations {
		if len(assetNames) == 0 {
			klog.Infof("All prerequisite assets are applied")
			return nil
		}
		results := resourceapply.ApplyDirectly(
			ctx,
			resourceapply.NewKubeClientHolder(kubeClient),
			recorder,
			resourceapply.NewResourceCache(),
			assetFunc,
			assetNames...,
		)

		errs = errs[:0]
		for _, result := range results {
			if result.Error != nil {
				errs = append(errs, fmt.Errorf("%s: %w", result.File, result.Error))
				continue
			}
			klog.V(2).Infof("Applied prerequisite asset %s (changed=%v)", result.File, result.Changed)
			// Remove successfully applied assets from the list
			for i, file := range assetNames {
				if file == result.File {
					assetNames = append(assetNames[:i], assetNames[i+1:]...)
					break
				}
			}
		}
		if len(errs) != 0 {
			klog.Warningf("Failed to apply some prerequisites: %v", errs)
		}
		if len(assetNames) != 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delayIteration):
			}
		}
	}
	return fmt.Errorf("failed to apply some prerequisites: %v", errs)

}
