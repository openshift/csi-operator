package csidriverdeployment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/ghodss/yaml"
	"github.com/openshift/csi-operator/pkg/apis"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	fakeTime = time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)
)

// TestController runs all tests in testdata/ directory without any error. It creates fake client with all in-*
// objects, runs one controller sync and compares created/deleted/updated objects with all out-* files.
func TestController(t *testing.T) {
	apis.AddToScheme(scheme.Scheme)

	files, err := ioutil.ReadDir("testdata")
	if err != nil {
		t.Fatalf("cannot list tesdata/: %s", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".") {
			continue
		}
		if file.IsDir() {
			t.Run(file.Name(), func(t *testing.T) {
				testDirectory(t, filepath.Join("testdata", file.Name()))
			})
		}
	}
}

// TestController runs all tests in testdata/ directory with injected errors. Each Client call fails exactly once
// (checked by stack trace). It creates fake client with all in-* objects, runs controller sync until it succeeds
// and compares created/deleted/updated objects with all out-* files.
// Goal of this test is to check that the controller eventually reaches the same result as if there were no errors.
func TestControllerErrors(t *testing.T) {
	apis.AddToScheme(scheme.Scheme)

	files, err := ioutil.ReadDir("testdata")
	if err != nil {
		t.Fatalf("cannot list tesdata/: %s", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".") {
			continue
		}
		if file.IsDir() {
			t.Run(file.Name(), func(t *testing.T) {
				testDirectoryErrors(t, filepath.Join("testdata", file.Name()))
			})
		}
	}
}

// parseFile parses *one* object out of a YAML file and returns it
func parseFile(path string) (runtime.Object, error) {
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj, _, err := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer().Decode(content, nil, nil)
	return obj, err
}

// parseDirectory parses *one* object out of each yaml file in a directory returns array of them
func parseDirectory(t *testing.T, path string) (inObjects, outObjects []runtime.Object) {
	inObjects = []runtime.Object{}
	outObjects = []runtime.Object{}

	files, err := ioutil.ReadDir(path)
	if err != nil {
		t.Fatalf("cannot list %s: %s", path, err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".") {
			continue
		}
		if file.IsDir() {
			t.Errorf("subdirectory %s is not allowed in %s", file.Name(), path)
		}

		switch {
		case strings.HasPrefix(file.Name(), "in-"):
			obj, err := parseFile(filepath.Join(path, file.Name()))
			if err != nil {
				t.Errorf("%s", err)
				continue
			}
			inObjects = append(inObjects, obj)

		case strings.HasPrefix(file.Name(), "out-"):
			obj, err := parseFile(filepath.Join(path, file.Name()))
			if err != nil {
				t.Errorf("%s", err)
				continue
			}
			outObjects = append(outObjects, obj)

		case strings.HasSuffix(file.Name(), ".txt"):
		case strings.HasSuffix(file.Name(), ".md"):
			// Ignore text files
		default:
			t.Errorf("file %s/%s has unknown prefix", path, file.Name())
		}
	}
	return
}

// testDirectory populates fake client with in-* files from a directory, runs one controller sync
// and compares created/updated/deleted objects with out-* files in the directory.
func testDirectory(t *testing.T, path string) {
	t.Logf("processing directory %s", path)
	inObjects, outObjects := parseDirectory(t, path)

	cr := findCR(inObjects)
	if cr == nil {
		t.Errorf("could not find CSIDriverDeployment in input objects in %s", path)
		return
	}
	client, err := newFakeClient(inObjects, nil)
	if err != nil {
		t.Error(err)
		return
	}

	reconciler := ReconcileCSIDriverDeployment{
		client:   client,
		config:   testConfig,
		recorder: record.NewFakeRecorder(1000),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		},
	}

	_, err = reconciler.Reconcile(req)
	if err != nil {
		t.Errorf("unexpected reconcile error: %s", err)
	}

	checkObjects(t, client, outObjects)
}

// testDirectory populates fake client with in-* files from a directory, runs controller sync until it succeeds
// and compares created/updated/deleted objects with out-* files in the directory.
// The client provided to the controllers fails the first call and then every second call. This should test
// all error handling.
func testDirectoryErrors(t *testing.T, path string) {
	t.Logf("processing directory %s", path)
	inObjects, outObjects := parseDirectory(t, path)

	cr := findCR(inObjects)
	if cr == nil {
		t.Errorf("could not find CSIDriverDeployment in input objects in %s", path)
		return
	}
	client, err := newFakeClient(inObjects, newStableErrorInjector(t))
	if err != nil {
		t.Error(err)
		return
	}

	reconciler := ReconcileCSIDriverDeployment{
		client:   client,
		config:   testConfig,
		recorder: record.NewFakeRecorder(1000),
	}

	// Reconcile until it succeeds
	for attempts := 0; attempts < 100; attempts++ {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: cr.Namespace,
				Name:      cr.Name,
			},
		}
		_, err = reconciler.Reconcile(req)
		if err != nil {
			t.Logf("sync %d failed with: %s", attempts, err)
		} else {
			t.Logf("controller sync succeeded after %d attempts", attempts)
			break
		}
	}
	if err != nil {
		t.Errorf("unexpected reconcile error: %s", err)
	}

	checkObjects(t, client, outObjects)
}

func findCR(inObjects []runtime.Object) *v1alpha1.CSIDriverDeployment {
	var cr *v1alpha1.CSIDriverDeployment
	for _, o := range inObjects {
		var ok bool
		if cr, ok = o.(*v1alpha1.CSIDriverDeployment); ok {
			return cr
		}
	}
	return nil
}

func checkObjects(t *testing.T, client *fakeClient, expectedObjects []runtime.Object) {
	// Get list of output objects in the same way as `client` has them for easy comparison,
	// i.e. using dummy fakeClient.
	expectedClient, err := newFakeClient(expectedObjects, nil)
	if err != nil {
		t.Error(err)
		return
	}

	// Compare the objects
	expectedObjs := expectedClient.objects
	gotObjs := client.objects
	for k, gotObj := range gotObjs {
		expectedObj, found := expectedObjs[k]
		if !found {
			t.Errorf("unexpected object %+v created:\n%s", k, objectYAML(gotObj))
			continue
		}

		// gotObj does not have TypeMeta. Fill it.
		buf := new(bytes.Buffer)
		err := serializer.NewCodecFactory(scheme.Scheme).LegacyCodec(corev1.SchemeGroupVersion, appsv1.SchemeGroupVersion, storagev1.SchemeGroupVersion, rbacv1.SchemeGroupVersion, v1alpha1.SchemeGroupVersion).Encode(gotObj, buf)
		if err != nil {
			t.Error(err)
			continue
		}
		gotObj, _, err = serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer().Decode(buf.Bytes(), nil, nil)
		if err != nil {
			t.Error(err)
			continue
		}

		sanitize(gotObj)

		if !equality.Semantic.DeepEqual(expectedObj, gotObj) {
			t.Errorf("unexpected object %+v content:\n%s", k, diff.ObjectDiff(expectedObj, gotObj))
		}
		// Delete processed objects to keep track of the unprocessed ones.
		delete(expectedObjs, k)
	}
	// Unprocessed objects.
	for k := range expectedObjs {
		t.Errorf("expected object %+v but none was created", k)
	}
}

// objectYAML prints YAML of an object
func objectYAML(obj runtime.Object) string {
	objString := ""
	j, err := json.Marshal(obj)
	if err != nil {
		objString = err.Error()
	} else {
		y, err := yaml.JSONToYAML(j)
		if err != nil {
			objString = err.Error()
		} else {
			objString = string(y)
		}
	}
	return objString
}

// sanitize clears any fields that are hard to test, such as timestamps.
func sanitize(object runtime.Object) {
	switch object.(type) {
	case *v1alpha1.CSIDriverDeployment:
		cr := object.(*v1alpha1.CSIDriverDeployment)
		for i := range cr.Status.Conditions {
			cr.Status.Conditions[i].LastTransitionTime = metav1.NewTime(fakeTime)
		}
	}
}

// stableErrorInjector fails every call exactly once.
// It uses call stack to check what calls it has already failed.
type stableErrorInjector struct {
	t     *testing.T
	calls sets.String
}

func newStableErrorInjector(t *testing.T) *stableErrorInjector {
	return &stableErrorInjector{
		t:     t,
		calls: sets.NewString(),
	}
}
func (s *stableErrorInjector) shouldFail(method string, object runtime.Object) error {
	_, file, line, _ := goruntime.Caller(2)
	callID := fmt.Sprintf("%s:%d", file, line)
	if s.calls.Has(callID) {
		return nil
	}
	s.calls.Insert(callID)
	return fmt.Errorf("call %s failed", callID)
}
