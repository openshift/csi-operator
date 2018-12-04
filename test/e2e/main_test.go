package e2e

import (
	"context"
	"log"
	"testing"
	"time"

	f "github.com/operator-framework/operator-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	dynclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	csiOperatorNamespace = "openshift-csi-operator"
	apiTimeout           = 10 * time.Second
)

func TestMain(m *testing.M) {
	log.Printf("TestMain started\n")
	f.MainEntry(m)
}

func collectLogs(t *testing.T, client f.FrameworkClient, namespace string) {
	if t.Failed() {
		// Collect logs from all csi-operator pods
		podList := &corev1.PodList{}
		opts := &dynclient.ListOptions{
			Namespace: csiOperatorNamespace,
		}

		ctx, cancel := testContext()
		defer cancel()
		err := client.List(ctx, opts, podList)
		if err != nil {
			t.Logf("failed to list pods in %s: %s", csiOperatorNamespace, err)
		}
		for _, pod := range podList.Items {
			t.Logf("csi-operator pod %s/%s logs:", pod.Namespace, pod.Name)
			req := f.Global.KubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
			body, err := req.Do().Raw()
			if err != nil {
				t.Logf("  failed to get logs: %s", err)
			} else {
				t.Log(string(body))
			}
		}

		// List all events in the test namespace
		eventList := &corev1.EventList{}
		opts = &dynclient.ListOptions{
			Namespace: namespace,
		}
		ctx, cancel = testContext()
		defer cancel()
		err = client.List(ctx, opts, eventList)
		if err != nil {
			t.Logf("failed to list events in %s: %s", namespace, err)
		}
		for _, e := range eventList.Items {
			t.Logf("event first time %s, count %d, type %s, reason %s: %s", e.FirstTimestamp, e.Count, e.Type, e.Reason, e.Message)
		}
	}
}

func testContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), apiTimeout)
}
