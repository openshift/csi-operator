package csidriverdeployment

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type errorInjector interface {
	shouldFail(method string, obj runtime.Object) error
}

type storageKey struct {
	Namespace string
	Name      string
	Kind      string
}

type fakeClient struct {
	objects       map[storageKey]runtime.Object
	errorInjector errorInjector
}

var _ client.Client = &fakeClient{}

func getKey(obj runtime.Object) (storageKey, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return storageKey{}, err
	}
	gvk, err := apiutil.GVKForObject(obj, scheme.Scheme)
	if err != nil {
		return storageKey{}, err
	}
	return storageKey{
		Name:      accessor.GetName(),
		Namespace: accessor.GetNamespace(),
		Kind:      gvk.Kind,
	}, nil
}

func newFakeClient(initialObjects []runtime.Object, errorInjector errorInjector) (*fakeClient, error) {
	client := &fakeClient{
		objects:       map[storageKey]runtime.Object{},
		errorInjector: errorInjector,
	}

	for _, obj := range initialObjects {
		key, err := getKey(obj)
		if err != nil {
			return nil, err
		}
		client.objects[key] = obj
	}
	return client, nil
}

func (f *fakeClient) Get(ctx context.Context, key client.ObjectKey, obj runtime.Object) error {
	if f.errorInjector != nil {
		if err := f.errorInjector.shouldFail("Get", obj); err != nil {
			return err
		}
	}

	gvk, err := apiutil.GVKForObject(obj, scheme.Scheme)
	if err != nil {
		return err
	}
	k := storageKey{
		Name:      key.Name,
		Namespace: key.Namespace,
		Kind:      gvk.Kind,
	}
	o, found := f.objects[k]
	if !found {
		gvr := schema.GroupResource{
			Group:    gvk.Group,
			Resource: gvk.Kind,
		}
		return errors.NewNotFound(gvr, key.Name)
	}

	j, err := json.Marshal(o)
	if err != nil {
		return err
	}
	decoder := scheme.Codecs.UniversalDecoder()
	_, _, err = decoder.Decode(j, nil, obj)
	return err
}

func (f *fakeClient) List(ctx context.Context, opts *client.ListOptions, list runtime.Object) error {
	if f.errorInjector != nil {
		if err := f.errorInjector.shouldFail("List", list); err != nil {
			return err
		}
	}
	switch list.(type) {
	case *storagev1.StorageClassList:
		return f.listStorageClasses(list.(*storagev1.StorageClassList))
	default:
		return fmt.Errorf("Unknown type: %s", reflect.TypeOf(list))
	}
}

func (f *fakeClient) listStorageClasses(list *storagev1.StorageClassList) error {
	for k, v := range f.objects {
		if k.Kind == "StorageClass" {
			list.Items = append(list.Items, *v.(*storagev1.StorageClass))
		}
	}
	return nil
}

func (f *fakeClient) Create(ctx context.Context, obj runtime.Object) error {
	if f.errorInjector != nil {
		if err := f.errorInjector.shouldFail("Create", obj); err != nil {
			return err
		}
	}
	k, err := getKey(obj)
	if err != nil {
		return err
	}
	_, found := f.objects[k]
	if found {
		gvk, err := apiutil.GVKForObject(obj, scheme.Scheme)
		if err != nil {
			return err
		}
		gvr := schema.GroupResource{
			Group:    gvk.Group,
			Resource: gvk.Kind,
		}
		return errors.NewAlreadyExists(gvr, k.Name)
	}
	f.objects[k] = obj
	return nil
}

func (f *fakeClient) Delete(ctx context.Context, obj runtime.Object, opts ...client.DeleteOptionFunc) error {
	if len(opts) > 0 {
		return fmt.Errorf("delete options are not supported")
	}
	if f.errorInjector != nil {
		if err := f.errorInjector.shouldFail("Delete", obj); err != nil {
			return err
		}
	}

	k, err := getKey(obj)
	if err != nil {
		return err
	}
	_, found := f.objects[k]
	if !found {
		gvk, err := apiutil.GVKForObject(obj, scheme.Scheme)
		if err != nil {
			return err
		}
		gvr := schema.GroupResource{
			Group:    gvk.Group,
			Resource: gvk.Kind,
		}
		return errors.NewNotFound(gvr, k.Name)
	}
	delete(f.objects, k)
	return nil
}

func (f *fakeClient) Update(ctx context.Context, obj runtime.Object) error {
	if f.errorInjector != nil {
		if err := f.errorInjector.shouldFail("Update", obj); err != nil {
			return err
		}
	}
	k, err := getKey(obj)
	if err != nil {
		return err
	}
	_, found := f.objects[k]
	if !found {
		gvk, err := apiutil.GVKForObject(obj, scheme.Scheme)
		if err != nil {
			return err
		}
		gvr := schema.GroupResource{
			Group:    gvk.Group,
			Resource: gvk.Kind,
		}
		return errors.NewNotFound(gvr, k.Name)
	}
	f.objects[k] = obj
	return nil
}

func (f *fakeClient) Status() client.StatusWriter {
	return f
}
