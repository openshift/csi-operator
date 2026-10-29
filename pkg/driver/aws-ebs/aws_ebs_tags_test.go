package aws_ebs

import (
	"context"
	"reflect"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/utils/clock"
)

func extraTagsArgument(t *testing.T, deployment *appsv1.Deployment) string {
	t.Helper()
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "csi-driver" {
			for _, argument := range container.Args {
				if strings.HasPrefix(argument, "--extra-tags=") {
					return strings.TrimPrefix(argument, "--extra-tags=")
				}
			}
			return ""
		}
	}
	t.Fatal("csi-driver container not found")
	return ""
}

func TestCustomTagsApplyDeployment(t *testing.T) {
	tagA := configv1.AWSResourceTag{Key: "a", Value: "one"}
	tagB := configv1.AWSResourceTag{Key: "b", Value: "two"}
	tagC := configv1.AWSResourceTag{Key: "c", Value: "three"}
	type testCase struct {
		name        string
		before      []configv1.AWSResourceTag
		after       []configv1.AWSResourceTag
		wantBefore  string
		wantAfter   string
		wantUpdate  bool
		wantInvalid bool
	}
	tests := []testCase{
		{"identical unsorted order", []configv1.AWSResourceTag{tagB, tagA}, []configv1.AWSResourceTag{tagB, tagA}, "a=one,b=two", "a=one,b=two", false, false},
		{"add", []configv1.AWSResourceTag{tagA}, []configv1.AWSResourceTag{tagB, tagA}, "a=one", "a=one,b=two", true, false},
		{"remove", []configv1.AWSResourceTag{tagB, tagA}, []configv1.AWSResourceTag{tagA}, "a=one,b=two", "a=one", true, false},
		{"change value", []configv1.AWSResourceTag{tagA}, []configv1.AWSResourceTag{{Key: "a", Value: "changed"}}, "a=one", "a=changed", true, false},
		{"remove all nil", []configv1.AWSResourceTag{tagA}, nil, "a=one", "", true, false},
		{"remove all empty", []configv1.AWSResourceTag{tagA}, []configv1.AWSResourceTag{}, "a=one", "", true, false},
		{"nil to empty", nil, []configv1.AWSResourceTag{}, "", "", false, false},
		{"duplicate stable", []configv1.AWSResourceTag{tagB, {Key: "a", Value: "first"}, tagA}, []configv1.AWSResourceTag{{Key: "a", Value: "first"}, tagB, tagA}, "a=first,a=one,b=two", "a=first,a=one,b=two", false, false},
		{"duplicate precedence changes", []configv1.AWSResourceTag{tagA, {Key: "a", Value: "last"}}, []configv1.AWSResourceTag{{Key: "a", Value: "last"}, tagA}, "a=one,a=last", "a=last,a=one", true, false},
		{"whitespace collision", []configv1.AWSResourceTag{tagB, tagA, {Key: " a ", Value: " last "}}, []configv1.AWSResourceTag{tagA, tagB, {Key: " a ", Value: " last "}}, "a=one, a = last ,b=two", "a=one, a = last ,b=two", false, false},
		{"equals collision", []configv1.AWSResourceTag{tagB, {Key: "a=z", Value: "one"}, tagA}, []configv1.AWSResourceTag{{Key: "a=z", Value: "one"}, tagB, tagA}, "a=z=one,a=one,b=two", "a=z=one,a=one,b=two", false, false},
		{"whitespace equals precedence", []configv1.AWSResourceTag{tagA, {Key: " a =z", Value: " last "}}, []configv1.AWSResourceTag{{Key: " a =z", Value: " last "}, tagA}, "a=one, a =z= last ", " a =z= last ,a=one", true, false},
		{"empty key value", []configv1.AWSResourceTag{tagB, {Key: "", Value: ""}}, []configv1.AWSResourceTag{{Key: "", Value: ""}, tagB}, "=,b=two", "=,b=two", false, false},
		{"comma value fallback", []configv1.AWSResourceTag{tagB, {Key: "a", Value: "one,b=last"}}, []configv1.AWSResourceTag{{Key: "a", Value: "one,b=last"}, tagB}, "b=two,a=one,b=last", "a=one,b=last,b=two", true, false},
		{"comma key fallback", []configv1.AWSResourceTag{tagB, {Key: "a=x,c", Value: "last"}}, []configv1.AWSResourceTag{{Key: "a=x,c", Value: "last"}, tagB}, "b=two,a=x,c=last", "a=x,c=last,b=two", true, false},
		{"malformed fallback", []configv1.AWSResourceTag{tagB, {Key: "a", Value: "one,broken"}}, []configv1.AWSResourceTag{{Key: "a", Value: "one,broken"}, tagB}, "b=two,a=one,broken", "a=one,broken,b=two", true, true},
		{"malformed key fallback", []configv1.AWSResourceTag{tagB, {Key: "broken,a", Value: "one"}}, []configv1.AWSResourceTag{{Key: "broken,a", Value: "one"}, tagB}, "b=two,broken,a=one", "broken,a=one,b=two", true, true},
		{"comma identical control", []configv1.AWSResourceTag{tagB, {Key: "a", Value: "one,b=last"}}, []configv1.AWSResourceTag{tagB, {Key: "a", Value: "one,b=last"}}, "b=two,a=one,b=last", "b=two,a=one,b=last", false, false},
	}
	for _, permutation := range [][]configv1.AWSResourceTag{
		{tagA, tagB, tagC}, {tagA, tagC, tagB}, {tagB, tagA, tagC},
		{tagB, tagC, tagA}, {tagC, tagA, tagB}, {tagC, tagB, tagA},
	} {
		tests = append(tests, testCase{"permutation " + permutation[0].Key + permutation[1].Key + permutation[2].Key, []configv1.AWSResourceTag{tagC, tagB, tagA}, permutation, "a=one,b=two,c=three", "a=one,b=two,c=three", false, false})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			operatorCR := clients.GetFakeOperatorCR()
			client := clients.NewFakeClients("clusters-test", operatorCR)
			hook, _ := withCustomTags(client)
			render := func(tags []configv1.AWSResourceTag, want string) *appsv1.Deployment {
				t.Helper()
				infra := getInfrastructure()
				infra.Status.PlatformStatus.AWS.ResourceTags = tags
				original := infra.DeepCopy()
				if err := client.GetInfraInformer().Informer().GetIndexer().Update(infra); err != nil {
					t.Fatal(err)
				}
				deployment := getTestDeployment()
				deployment.Generation = 7
				if err := hook(&operatorCR.Spec.OperatorSpec, deployment); err != nil {
					t.Fatal(err)
				}
				cached, err := client.GetInfraInformer().Lister().Get(infrastructureName)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(cached, original) || !reflect.DeepEqual(infra, original) {
					t.Fatal("hook mutated informer-owned infrastructure")
				}
				actual := extraTagsArgument(t, deployment)
				if actual != want {
					t.Fatalf("extra-tags = %q, want %q", actual, want)
				}
				rawPairs := make([]string, 0, len(tags))
				for _, tag := range tags {
					rawPairs = append(rawPairs, tag.Key+"="+tag.Value)
				}
				var originalMap, actualMap map[string]string
				originalErr := cliflag.NewMapStringString(&originalMap).Set(strings.Join(rawPairs, ","))
				actualErr := cliflag.NewMapStringString(&actualMap).Set(actual)
				if (originalErr != nil) != test.wantInvalid || (actualErr != nil) != test.wantInvalid || !reflect.DeepEqual(originalMap, actualMap) {
					t.Fatalf("parser semantics changed: original %v (%v), rendered %v (%v)", originalMap, originalErr, actualMap, actualErr)
				}
				return deployment
			}
			before := render(test.before, test.wantBefore)
			after := render(test.after, test.wantAfter)
			if sameTemplate := reflect.DeepEqual(before.Spec.Template, after.Spec.Template); sameTemplate == test.wantUpdate {
				t.Fatalf("template equality = %v, want update = %v", sameTemplate, test.wantUpdate)
			}
			kubeClient := fake.NewSimpleClientset()
			recorder := events.NewInMemoryRecorder("test", clock.RealClock{})
			created, changed, err := resourceapply.ApplyDeployment(context.Background(), kubeClient.AppsV1(), recorder, before, before.Generation)
			if err != nil || !changed {
				t.Fatalf("initial apply: changed %v, error %v", changed, err)
			}
			kubeClient.ClearActions()
			applied, changed, err := resourceapply.ApplyDeployment(context.Background(), kubeClient.AppsV1(), recorder, after, created.Generation)
			if err != nil || changed != test.wantUpdate {
				t.Fatalf("second apply: changed %v, want %v, error %v", changed, test.wantUpdate, err)
			}
			beforeHash := created.Annotations["operator.openshift.io/spec-hash"]
			afterHash := applied.Annotations["operator.openshift.io/spec-hash"]
			if beforeHash == "" || afterHash == "" || (beforeHash != afterHash) != test.wantUpdate {
				t.Fatalf("unexpected spec hashes: %q -> %q", beforeHash, afterHash)
			}
			updates := 0
			for _, action := range kubeClient.Actions() {
				if action.GetVerb() == "update" && action.GetResource().Resource == "deployments" {
					updates++
				} else if action.GetVerb() != "get" {
					t.Fatalf("unexpected API action: %v", action)
				}
			}
			if (test.wantUpdate && updates != 1) || (!test.wantUpdate && updates != 0) {
				t.Fatalf("Deployment updates = %d, want update = %v", updates, test.wantUpdate)
			}
			stored, err := kubeClient.AppsV1().Deployments(after.Namespace).Get(context.Background(), after.Name, metav1.GetOptions{})
			if err != nil || !reflect.DeepEqual(stored.Spec, after.Spec) {
				t.Fatalf("stored spec does not match required spec: %v", err)
			}
		})
	}
}

func TestCustomTagsInfrastructureFallbacks(t *testing.T) {
	for _, name := range []string{"missing", "nil platform", "non AWS", "nil tags", "empty tags"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			operatorCR := clients.GetFakeOperatorCR()
			client := clients.NewFakeClients("clusters-test", operatorCR)
			infra := getInfrastructure()
			switch name {
			case "nil platform":
				infra.Status.PlatformStatus = nil
			case "non AWS":
				infra.Status.PlatformStatus = &configv1.PlatformStatus{Type: configv1.AzurePlatformType}
			case "empty tags":
				infra.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{}
			}
			if name != "missing" {
				if err := client.GetInfraInformer().Informer().GetIndexer().Add(infra); err != nil {
					t.Fatal(err)
				}
			}
			hook, _ := withCustomTags(client)
			deployment := getTestDeployment()
			original := deployment.DeepCopy()
			err := hook(&operatorCR.Spec.OperatorSpec, deployment)
			if name == "missing" {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected NotFound, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(deployment, original) {
				t.Fatal("fallback changed deployment")
			}
		})
	}
}
