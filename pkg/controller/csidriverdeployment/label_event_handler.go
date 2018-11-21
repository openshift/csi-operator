/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package csidriverdeployment

import (
	"sigs.k8s.io/controller-runtime/pkg/handler"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// OwnerLabelNamespace is name of label with namespace of owner CSIDriverDeployment.
	OwnerLabelNamespace = "csidriver.storage.openshift.io/owner-namespace"
	// OwnerLabelName is name of label with name of owner CSIDriverDeployment.
	OwnerLabelName = "csidriver.storage.openshift.io/owner-name"
)

var _ handler.EventHandler = &EnqueueRequestForLabels{}

// EnqueueRequestForLabels enqueues Requests for the owner of an object. The owner is determined based on
// labels instead of ObjectMeta.OwnerReferences, because OwnerReferences don't work for non-namespaced objects.
type EnqueueRequestForLabels struct {
}

// Create implements EventHandler
func (e *EnqueueRequestForLabels) Create(evt event.CreateEvent, q workqueue.RateLimitingInterface) {
	req := e.getOwnerReconcileRequest(evt.Meta)
	if req != nil {
		q.Add(*req)
	}
}

// Update implements EventHandler
func (e *EnqueueRequestForLabels) Update(evt event.UpdateEvent, q workqueue.RateLimitingInterface) {
	req := e.getOwnerReconcileRequest(evt.MetaOld)
	if req != nil {
		q.Add(*req)
	}
	req = e.getOwnerReconcileRequest(evt.MetaNew)
	if req != nil {
		q.Add(*req)
	}
}

// Delete implements EventHandler
func (e *EnqueueRequestForLabels) Delete(evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
	req := e.getOwnerReconcileRequest(evt.Meta)
	if req != nil {
		q.Add(*req)
	}
}

// Generic implements EventHandler
func (e *EnqueueRequestForLabels) Generic(evt event.GenericEvent, q workqueue.RateLimitingInterface) {
	req := e.getOwnerReconcileRequest(evt.Meta)
	if req != nil {
		q.Add(*req)
	}
}

// getOwnerReconcileRequest looks at object and returns a slice of reconcile.Request to reconcile
// owners of object that match e.OwnerType.
func (e *EnqueueRequestForLabels) getOwnerReconcileRequest(object metav1.Object) *reconcile.Request {
	labels := object.GetLabels()
	namespace, foundNamespace := labels[OwnerLabelNamespace]
	name, foundName := labels[OwnerLabelName]

	if foundName && foundNamespace {
		return &reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      name,
			},
		}
	}
	return nil
}
