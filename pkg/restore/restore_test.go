/*
Copyright 2017, 2019 the Velero contributors.

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

package restore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	discoveryfake "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"

	api "github.com/heptio/velero/pkg/apis/velero/v1"
	pkgclient "github.com/heptio/velero/pkg/client"
	"github.com/heptio/velero/pkg/discovery"
	"github.com/heptio/velero/pkg/kuberesource"
	"github.com/heptio/velero/pkg/plugin/velero"
	"github.com/heptio/velero/pkg/test"
	"github.com/heptio/velero/pkg/util/collections"
	velerotest "github.com/heptio/velero/pkg/util/test"
	"github.com/heptio/velero/pkg/volume"
)

func TestPrioritizeResources(t *testing.T) {
	tests := []struct {
		name         string
		apiResources map[string][]string
		priorities   []string
		includes     []string
		excludes     []string
		expected     []string
	}{
		{
			name: "priorities & ordering are correctly applied",
			apiResources: map[string][]string{
				"v1": {"aaa", "bbb", "configmaps", "ddd", "namespaces", "ooo", "pods", "sss"},
			},
			priorities: []string{"namespaces", "configmaps", "pods"},
			includes:   []string{"*"},
			expected:   []string{"namespaces", "configmaps", "pods", "aaa", "bbb", "ddd", "ooo", "sss"},
		},
		{
			name: "includes are correctly applied",
			apiResources: map[string][]string{
				"v1": {"aaa", "bbb", "configmaps", "ddd", "namespaces", "ooo", "pods", "sss"},
			},
			priorities: []string{"namespaces", "configmaps", "pods"},
			includes:   []string{"namespaces", "aaa", "sss"},
			expected:   []string{"namespaces", "aaa", "sss"},
		},
		{
			name: "excludes are correctly applied",
			apiResources: map[string][]string{
				"v1": {"aaa", "bbb", "configmaps", "ddd", "namespaces", "ooo", "pods", "sss"},
			},
			priorities: []string{"namespaces", "configmaps", "pods"},
			includes:   []string{"*"},
			excludes:   []string{"ooo", "pods"},
			expected:   []string{"namespaces", "configmaps", "aaa", "bbb", "ddd", "sss"},
		},
	}

	logger := velerotest.NewLogger()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			discoveryClient := &test.DiscoveryClient{
				FakeDiscovery: kubefake.NewSimpleClientset().Discovery().(*discoveryfake.FakeDiscovery),
			}

			helper, err := discovery.NewHelper(discoveryClient, logger)
			require.NoError(t, err)

			// add all the test case's API resources to the discovery client
			for gvString, resources := range tc.apiResources {
				gv, err := schema.ParseGroupVersion(gvString)
				require.NoError(t, err)

				for _, resource := range resources {
					discoveryClient.WithAPIResource(&test.APIResource{
						Group:   gv.Group,
						Version: gv.Version,
						Name:    resource,
					})
				}
			}

			require.NoError(t, helper.Refresh())

			includesExcludes := collections.NewIncludesExcludes().Includes(tc.includes...).Excludes(tc.excludes...)

			result, err := prioritizeResources(helper, tc.priorities, includesExcludes, logger)
			require.NoError(t, err)

			require.Equal(t, len(tc.expected), len(result))

			for i := range result {
				if e, a := tc.expected[i], result[i].Resource; e != a {
					t.Errorf("index %d, expected %s, got %s", i, e, a)
				}
			}
		})
	}
}

func TestRestoringPVsWithoutSnapshots(t *testing.T) {
	pv := `apiVersion: v1
kind: PersistentVolume
metadata:
  annotations:
    EXPORT_block: "\nEXPORT\n{\n\tExport_Id = 1;\n\tPath = /export/pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce;\n\tPseudo
      = /export/pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce;\n\tAccess_Type = RW;\n\tSquash
      = no_root_squash;\n\tSecType = sys;\n\tFilesystem_id = 1.1;\n\tFSAL {\n\t\tName
      = VFS;\n\t}\n}\n"
    Export_Id: "1"
    Project_Id: "0"
    Project_block: ""
    Provisioner_Id: 5fdf4025-78a5-11e8-9ece-0242ac110004
    kubernetes.io/createdby: nfs-dynamic-provisioner
    pv.kubernetes.io/provisioned-by: example.com/nfs
    volume.beta.kubernetes.io/mount-options: vers=4.1
  creationTimestamp: 2018-06-25T18:27:35Z
  finalizers:
  - kubernetes.io/pv-protection
  name: pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
  resourceVersion: "2576"
  selfLink: /api/v1/persistentvolumes/pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
  uid: 6ecd24e4-78a5-11e8-a0d8-e2ad1e9734ce
spec:
  accessModes:
  - ReadWriteMany
  capacity:
    storage: 1Mi
  claimRef:
    apiVersion: v1
    kind: PersistentVolumeClaim
    name: nfs
    namespace: default
    resourceVersion: "2565"
    uid: 6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
  nfs:
    path: /export/pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
    server: 10.103.235.254
  storageClassName: example-nfs
status:
  phase: Bound`

	pvc := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  annotations:
    control-plane.alpha.kubernetes.io/leader: '{"holderIdentity":"5fdf5572-78a5-11e8-9ece-0242ac110004","leaseDurationSeconds":15,"acquireTime":"2018-06-25T18:27:35Z","renewTime":"2018-06-25T18:27:37Z","leaderTransitions":0}'
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"annotations":{},"name":"nfs","namespace":"default"},"spec":{"accessModes":["ReadWriteMany"],"resources":{"requests":{"storage":"1Mi"}},"storageClassName":"example-nfs"}}
    pv.kubernetes.io/bind-completed: "yes"
    pv.kubernetes.io/bound-by-controller: "yes"
    volume.beta.kubernetes.io/storage-provisioner: example.com/nfs
  creationTimestamp: 2018-06-25T18:27:28Z
  finalizers:
  - kubernetes.io/pvc-protection
  name: nfs
  namespace: default
  resourceVersion: "2578"
  selfLink: /api/v1/namespaces/default/persistentvolumeclaims/nfs
  uid: 6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
spec:
  accessModes:
  - ReadWriteMany
  resources:
    requests:
      storage: 1Mi
  storageClassName: example-nfs
  volumeName: pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce
status:
  accessModes:
  - ReadWriteMany
  capacity:
    storage: 1Mi
  phase: Bound`

	tests := []struct {
		name                          string
		haveSnapshot                  bool
		reclaimPolicy                 string
		expectPVCVolumeName           bool
		expectedPVCAnnotationsMissing sets.String
		expectPVCreation              bool
		expectPVFound                 bool
	}{
		{
			name:                "backup has snapshot, reclaim policy delete, no existing PV found",
			haveSnapshot:        true,
			reclaimPolicy:       "Delete",
			expectPVCVolumeName: true,
			expectPVCreation:    true,
		},
		{
			name:                "backup has snapshot, reclaim policy delete, existing PV found",
			haveSnapshot:        true,
			reclaimPolicy:       "Delete",
			expectPVCVolumeName: true,
			expectPVCreation:    false,
			expectPVFound:       true,
		},
		{
			name:                "backup has snapshot, reclaim policy retain, no existing PV found",
			haveSnapshot:        true,
			reclaimPolicy:       "Retain",
			expectPVCVolumeName: true,
			expectPVCreation:    true,
		},
		{
			name:                "backup has snapshot, reclaim policy retain, existing PV found",
			haveSnapshot:        true,
			reclaimPolicy:       "Retain",
			expectPVCVolumeName: true,
			expectPVCreation:    false,
			expectPVFound:       true,
		},
		{
			name:                "backup has snapshot, reclaim policy retain, existing PV found",
			haveSnapshot:        true,
			reclaimPolicy:       "Retain",
			expectPVCVolumeName: true,
			expectPVCreation:    false,
			expectPVFound:       true,
		},
		{
			name:                          "no snapshot, reclaim policy delete, no existing PV",
			haveSnapshot:                  false,
			reclaimPolicy:                 "Delete",
			expectPVCVolumeName:           false,
			expectedPVCAnnotationsMissing: sets.NewString("pv.kubernetes.io/bind-completed", "pv.kubernetes.io/bound-by-controller"),
		},
		{
			name:                "no snapshot, reclaim policy retain, no existing PV found",
			haveSnapshot:        false,
			reclaimPolicy:       "Retain",
			expectPVCVolumeName: true,
			expectPVCreation:    true,
		},
		{
			name:                "no snapshot, reclaim policy retain, existing PV found",
			haveSnapshot:        false,
			reclaimPolicy:       "Retain",
			expectPVCVolumeName: true,
			expectPVCreation:    false,
			expectPVFound:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dynamicFactory := &velerotest.FakeDynamicFactory{}
			gv := schema.GroupVersion{Group: "", Version: "v1"}

			pvClient := &velerotest.FakeDynamicClient{}
			defer pvClient.AssertExpectations(t)

			pvResource := metav1.APIResource{Name: "persistentvolumes", Namespaced: false}
			dynamicFactory.On("ClientForGroupVersionResource", gv, pvResource, "").Return(pvClient, nil)

			pvcClient := &velerotest.FakeDynamicClient{}
			defer pvcClient.AssertExpectations(t)

			pvcResource := metav1.APIResource{Name: "persistentvolumeclaims", Namespaced: true}
			dynamicFactory.On("ClientForGroupVersionResource", gv, pvcResource, "default").Return(pvcClient, nil)

			obj, _, err := scheme.Codecs.UniversalDecoder(v1.SchemeGroupVersion).Decode([]byte(pv), nil, nil)
			require.NoError(t, err)
			pvObj, ok := obj.(*v1.PersistentVolume)
			require.True(t, ok)
			pvObj.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimPolicy(test.reclaimPolicy)
			pvBytes, err := json.Marshal(pvObj)
			require.NoError(t, err)

			obj, _, err = scheme.Codecs.UniversalDecoder(v1.SchemeGroupVersion).Decode([]byte(pvc), nil, nil)
			require.NoError(t, err)
			pvcObj, ok := obj.(*v1.PersistentVolumeClaim)
			require.True(t, ok)
			pvcBytes, err := json.Marshal(pvcObj)
			require.NoError(t, err)

			unstructuredPVCMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pvcObj)
			require.NoError(t, err)
			unstructuredPVC := &unstructured.Unstructured{Object: unstructuredPVCMap}

			nsClient := &velerotest.FakeNamespaceClient{}
			ns := newTestNamespace(pvcObj.Namespace).Namespace
			nsClient.On("Get", pvcObj.Namespace, mock.Anything).Return(ns, nil)

			backup := &api.Backup{}

			pvRestorer := new(mockPVRestorer)
			defer pvRestorer.AssertExpectations(t)

			ctx := &context{
				dynamicFactory: dynamicFactory,
				actions:        []resolvedAction{},
				fileSystem: velerotest.NewFakeFileSystem().
					WithFile("foo/resources/persistentvolumes/cluster/pv.json", pvBytes).
					WithFile("foo/resources/persistentvolumeclaims/default/pvc.json", pvcBytes),
				selector:                  labels.NewSelector(),
				resourceIncludesExcludes:  collections.NewIncludesExcludes(),
				namespaceIncludesExcludes: collections.NewIncludesExcludes(),
				prioritizedResources: []schema.GroupResource{
					kuberesource.PersistentVolumes,
					kuberesource.PersistentVolumeClaims,
				},
				restore: &api.Restore{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: api.DefaultNamespace,
						Name:      "my-restore",
					},
				},
				backup:          backup,
				log:             velerotest.NewLogger(),
				pvsToProvision:  sets.NewString(),
				pvRestorer:      pvRestorer,
				namespaceClient: nsClient,
				resourceClients: make(map[resourceClientKey]pkgclient.Dynamic),
				restoredItems:   make(map[velero.ResourceIdentifier]struct{}),
			}

			if test.haveSnapshot {
				ctx.volumeSnapshots = append(ctx.volumeSnapshots, &volume.Snapshot{
					Spec: volume.SnapshotSpec{
						PersistentVolumeName: "pvc-6a74b5af-78a5-11e8-a0d8-e2ad1e9734ce",
					},
					Status: volume.SnapshotStatus{
						ProviderSnapshotID: "snap",
					},
				})
			}

			unstructuredPVMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pvObj)
			require.NoError(t, err)
			unstructuredPV := &unstructured.Unstructured{Object: unstructuredPVMap}

			if test.expectPVFound {
				// Copy the PV so that later modifcations don't affect what's returned by our faked calls.
				inClusterPV := unstructuredPV.DeepCopy()
				pvClient.On("Get", inClusterPV.GetName(), metav1.GetOptions{}).Return(inClusterPV, nil)
				pvClient.On("Create", mock.Anything).Return(inClusterPV, k8serrors.NewAlreadyExists(kuberesource.PersistentVolumes, inClusterPV.GetName()))
				inClusterPVC := unstructuredPVC.DeepCopy()
				pvcClient.On("Get", pvcObj.Name, mock.Anything).Return(inClusterPVC, nil)
			}

			// Only set up the client expectation if the test has the proper prerequisites
			if test.haveSnapshot || test.reclaimPolicy != "Delete" {
				pvClient.On("Get", unstructuredPV.GetName(), metav1.GetOptions{}).Return(&unstructured.Unstructured{}, k8serrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumes"}, unstructuredPV.GetName()))
			}

			pvToRestore := unstructuredPV.DeepCopy()
			restoredPV := unstructuredPV.DeepCopy()

			if test.expectPVCreation {
				// just to ensure we have the data flowing correctly
				restoredPV.Object["foo"] = "bar"
				pvRestorer.On("executePVAction", pvToRestore).Return(restoredPV, nil)
			}

			resetMetadataAndStatus(unstructuredPV)
			addRestoreLabels(unstructuredPV, ctx.restore.Name, ctx.restore.Spec.BackupName)
			unstructuredPV.Object["foo"] = "bar"

			if test.expectPVCreation {
				createdPV := unstructuredPV.DeepCopy()
				pvClient.On("Create", unstructuredPV).Return(createdPV, nil)
			}

			// Restore PV
			warnings, errors := ctx.restoreResource("persistentvolumes", "", "foo/resources/persistentvolumes/cluster/")

			assert.Empty(t, warnings.Velero)
			assert.Empty(t, warnings.Namespaces)
			assert.Equal(t, Result{}, errors)
			assert.Empty(t, warnings.Cluster)

			// Prep PVC restore
			// Handle expectations
			if !test.expectPVCVolumeName {
				pvcObj.Spec.VolumeName = ""
			}
			for _, key := range test.expectedPVCAnnotationsMissing.List() {
				delete(pvcObj.Annotations, key)
			}

			// Recreate the unstructured PVC since the object was edited.
			unstructuredPVCMap, err = runtime.DefaultUnstructuredConverter.ToUnstructured(pvcObj)
			require.NoError(t, err)
			unstructuredPVC = &unstructured.Unstructured{Object: unstructuredPVCMap}

			resetMetadataAndStatus(unstructuredPVC)
			addRestoreLabels(unstructuredPVC, ctx.restore.Name, ctx.restore.Spec.BackupName)

			createdPVC := unstructuredPVC.DeepCopy()
			// just to ensure we have the data flowing correctly
			createdPVC.Object["foo"] = "bar"

			pvcClient.On("Create", unstructuredPVC).Return(createdPVC, nil)

			// Restore PVC
			warnings, errors = ctx.restoreResource("persistentvolumeclaims", "default", "foo/resources/persistentvolumeclaims/default/")

			assert.Empty(t, warnings.Velero)
			assert.Empty(t, warnings.Cluster)
			assert.Empty(t, warnings.Namespaces)
			assert.Equal(t, Result{}, errors)
		})
	}
}

type mockPVRestorer struct {
	mock.Mock
}

func (r *mockPVRestorer) executePVAction(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	args := r.Called(obj)
	return args.Get(0).(*unstructured.Unstructured), args.Error(1)
}

func TestResetMetadataAndStatus(t *testing.T) {
	tests := []struct {
		name        string
		obj         *unstructured.Unstructured
		expectedErr bool
		expectedRes *unstructured.Unstructured
	}{
		{
			name:        "no metadata causes error",
			obj:         NewTestUnstructured().Unstructured,
			expectedErr: true,
		},
		{
			name:        "keep name, namespace, labels, annotations only",
			obj:         NewTestUnstructured().WithMetadata("name", "blah", "namespace", "labels", "annotations", "foo").Unstructured,
			expectedErr: false,
			expectedRes: NewTestUnstructured().WithMetadata("name", "namespace", "labels", "annotations").Unstructured,
		},
		{
			name:        "don't keep status",
			obj:         NewTestUnstructured().WithMetadata().WithStatus().Unstructured,
			expectedErr: false,
			expectedRes: NewTestUnstructured().WithMetadata().Unstructured,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resetMetadataAndStatus(test.obj)

			if assert.Equal(t, test.expectedErr, err != nil) {
				assert.Equal(t, test.expectedRes, res)
			}
		})
	}
}

func TestIsCompleted(t *testing.T) {
	tests := []struct {
		name          string
		expected      bool
		content       string
		groupResource schema.GroupResource
		expectedErr   bool
	}{
		{
			name:          "Failed pods are complete",
			expected:      true,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"phase": "Failed"}}`,
			groupResource: schema.GroupResource{Group: "", Resource: "pods"},
		},
		{
			name:          "Succeeded pods are complete",
			expected:      true,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"phase": "Succeeded"}}`,
			groupResource: schema.GroupResource{Group: "", Resource: "pods"},
		},
		{
			name:          "Pending pods aren't complete",
			expected:      false,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"phase": "Pending"}}`,
			groupResource: schema.GroupResource{Group: "", Resource: "pods"},
		},
		{
			name:          "Running pods aren't complete",
			expected:      false,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"phase": "Running"}}`,
			groupResource: schema.GroupResource{Group: "", Resource: "pods"},
		},
		{
			name:          "Jobs without a completion time aren't complete",
			expected:      false,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}}`,
			groupResource: schema.GroupResource{Group: "batch", Resource: "jobs"},
		},
		{
			name:          "Jobs with a completion time are completed",
			expected:      true,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"completionTime": "bar"}}`,
			groupResource: schema.GroupResource{Group: "batch", Resource: "jobs"},
		},
		{
			name:          "Jobs with an empty completion time are not completed",
			expected:      false,
			content:       `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"ns","name":"pod1"}, "status": {"completionTime": ""}}`,
			groupResource: schema.GroupResource{Group: "batch", Resource: "jobs"},
		},
		{
			name:          "Something not a pod or a job may actually be complete, but we're not concerned with that",
			expected:      false,
			content:       `{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "ns"}, "status": {"completionTime": "bar", "phase":"Completed"}}`,
			groupResource: schema.GroupResource{Group: "", Resource: "namespaces"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := velerotest.UnstructuredOrDie(test.content)
			backup, err := isCompleted(u, test.groupResource)

			if assert.Equal(t, test.expectedErr, err != nil) {
				assert.Equal(t, test.expected, backup)
			}
		})
	}
}

func TestGetItemFilePath(t *testing.T) {
	res := getItemFilePath("root", "resource", "", "item")
	assert.Equal(t, "root/resources/resource/cluster/item.json", res)

	res = getItemFilePath("root", "resource", "namespace", "item")
	assert.Equal(t, "root/resources/resource/namespaces/namespace/item.json", res)
}

type testUnstructured struct {
	*unstructured.Unstructured
}

func NewTestUnstructured() *testUnstructured {
	obj := &testUnstructured{
		Unstructured: &unstructured.Unstructured{
			Object: make(map[string]interface{}),
		},
	}

	return obj
}

func (obj *testUnstructured) WithAPIVersion(v string) *testUnstructured {
	obj.Object["apiVersion"] = v
	return obj
}

func (obj *testUnstructured) WithKind(k string) *testUnstructured {
	obj.Object["kind"] = k
	return obj
}

func (obj *testUnstructured) WithMetadata(fields ...string) *testUnstructured {
	return obj.withMap("metadata", fields...)
}

func (obj *testUnstructured) WithSpec(fields ...string) *testUnstructured {
	if _, found := obj.Object["spec"]; found {
		panic("spec already set - you probably didn't mean to do this twice!")
	}
	return obj.withMap("spec", fields...)
}

func (obj *testUnstructured) WithStatus(fields ...string) *testUnstructured {
	return obj.withMap("status", fields...)
}

func (obj *testUnstructured) WithMetadataField(field string, value interface{}) *testUnstructured {
	return obj.withMapEntry("metadata", field, value)
}

func (obj *testUnstructured) WithSpecField(field string, value interface{}) *testUnstructured {
	return obj.withMapEntry("spec", field, value)
}

func (obj *testUnstructured) WithStatusField(field string, value interface{}) *testUnstructured {
	return obj.withMapEntry("status", field, value)
}

func (obj *testUnstructured) WithAnnotations(fields ...string) *testUnstructured {
	vals := map[string]string{}
	for _, field := range fields {
		vals[field] = "foo"
	}

	return obj.WithAnnotationValues(vals)
}

func (obj *testUnstructured) WithAnnotationValues(fieldVals map[string]string) *testUnstructured {
	annotations := make(map[string]interface{})
	for field, val := range fieldVals {
		annotations[field] = val
	}

	obj = obj.WithMetadataField("annotations", annotations)

	return obj
}

func (obj *testUnstructured) WithNamespace(ns string) *testUnstructured {
	return obj.WithMetadataField("namespace", ns)
}

func (obj *testUnstructured) WithName(name string) *testUnstructured {
	return obj.WithMetadataField("name", name)
}

func (obj *testUnstructured) ToJSON() []byte {
	bytes, err := json.Marshal(obj.Object)
	if err != nil {
		panic(err)
	}
	return bytes
}

func (obj *testUnstructured) withMap(name string, fields ...string) *testUnstructured {
	m := make(map[string]interface{})
	obj.Object[name] = m

	for _, field := range fields {
		m[field] = "foo"
	}

	return obj
}

func (obj *testUnstructured) withMapEntry(mapName, field string, value interface{}) *testUnstructured {
	var m map[string]interface{}

	if res, ok := obj.Unstructured.Object[mapName]; !ok {
		m = make(map[string]interface{})
		obj.Unstructured.Object[mapName] = m
	} else {
		m = res.(map[string]interface{})
	}

	m[field] = value

	return obj
}

func toUnstructured(objs ...runtime.Object) []unstructured.Unstructured {
	res := make([]unstructured.Unstructured, 0, len(objs))

	for _, obj := range objs {
		jsonObj, err := json.Marshal(obj)
		if err != nil {
			panic(err)
		}

		var unstructuredObj unstructured.Unstructured

		if err := json.Unmarshal(jsonObj, &unstructuredObj); err != nil {
			panic(err)
		}

		metadata := unstructuredObj.Object["metadata"].(map[string]interface{})

		delete(metadata, "creationTimestamp")

		delete(unstructuredObj.Object, "status")

		res = append(res, unstructuredObj)
	}

	return res
}

type testNamespace struct {
	*v1.Namespace
}

func newTestNamespace(name string) *testNamespace {
	return &testNamespace{
		Namespace: &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (ns *testNamespace) ToJSON() []byte {
	bytes, _ := json.Marshal(ns.Namespace)
	return bytes
}

type fakeVolumeSnapshotterGetter struct {
	fakeVolumeSnapshotter *velerotest.FakeVolumeSnapshotter
	volumeMap             map[velerotest.VolumeBackupInfo]string
	volumeID              string
}

func (r *fakeVolumeSnapshotterGetter) GetVolumeSnapshotter(provider string) (velero.VolumeSnapshotter, error) {
	if r.fakeVolumeSnapshotter == nil {
		r.fakeVolumeSnapshotter = &velerotest.FakeVolumeSnapshotter{
			RestorableVolumes: r.volumeMap,
			VolumeID:          r.volumeID,
		}
	}
	return r.fakeVolumeSnapshotter, nil
}
