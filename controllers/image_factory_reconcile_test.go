package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/imagefactory"
)

// imageFactoryFixture is a Machine-owned TalosConfig in a provisioned cluster, reconciled
// against a fake Image Factory.
type imageFactoryFixture struct {
	client     client.Client
	reconciler *TalosConfigReconciler
	factory    *fakeFactory
	key        types.NamespacedName
}

func newImageFactoryFixture(t *testing.T, spec bootstrapv1beta1.TalosConfigSpec, factory *fakeFactory) *imageFactoryFixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, capiv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	const owner = "machine-1"

	objects := []client.Object{
		&capiv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: testClusterName, Namespace: testNamespace},
			Spec:       capiv1.ClusterSpec{ControlPlaneEndpoint: capiv1.APIEndpoint{Host: "1.2.3.4", Port: 6443}},
			Status:     capiv1.ClusterStatus{Initialization: capiv1.ClusterInitializationStatus{InfrastructureProvisioned: ptr.To(true)}},
		},
		&capiv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: owner, Namespace: testNamespace, UID: "owner-uid"},
			Spec:       capiv1.MachineSpec{ClusterName: testClusterName, Version: "v1.34.0"},
		},
		&bootstrapv1beta1.TalosConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: owner, Namespace: testNamespace,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: capiv1.GroupVersion.String(), Kind: "Machine", Name: owner, UID: "owner-uid"}},
			},
			Spec: spec,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&bootstrapv1beta1.TalosConfig{}).WithObjects(objects...).Build()

	var api imagefactory.API
	if factory != nil {
		api = factory
	}

	return &imageFactoryFixture{
		client: c,
		reconciler: &TalosConfigReconciler{
			Client:       c,
			Log:          ctrl.Log.WithName("test"),
			Scheme:       scheme,
			ImageFactory: api,
		},
		factory: factory,
		key:     types.NamespacedName{Namespace: testNamespace, Name: owner},
	}
}

func (f *imageFactoryFixture) reconcile(t *testing.T) error {
	t.Helper()

	_, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key})

	return err
}

func (f *imageFactoryFixture) config(t *testing.T) *bootstrapv1beta1.TalosConfig {
	t.Helper()

	cfg := &bootstrapv1beta1.TalosConfig{}
	require.NoError(t, f.client.Get(context.Background(), f.key, cfg))

	return cfg
}

// bootstrapData returns the rendered machine configuration from the data secret.
func (f *imageFactoryFixture) bootstrapData(t *testing.T) string {
	t.Helper()

	cfg := f.config(t)
	require.NotEmpty(t, cfg.Status.DataSecretName, "bootstrap data must have been written")

	secret := &corev1.Secret{}
	require.NoError(t, f.client.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: cfg.Status.DataSecretName}, secret))

	return string(secret.Data["value"])
}

func TestReconcileRendersTheResolvedInstallerImage(t *testing.T) {
	t.Parallel()

	factory := newFakeFactory()
	f := newImageFactoryFixture(t, specWith("v1.14", "siderolabs/nvme-cli"), factory)

	require.NoError(t, f.reconcile(t))

	cfg := f.config(t)
	require.NotNil(t, cfg.Status.ImageFactory)
	assert.Equal(t, "v1.14.2", cfg.Status.ImageFactory.TalosVersion)
	assert.Equal(t, "sid-siderolabs/nvme-cli", cfg.Status.ImageFactory.SchematicID)
	assert.Equal(t, "factory.example.test/metal-installer/sid-siderolabs/nvme-cli:v1.14.2", cfg.Status.ImageFactory.InstallerImage)

	cond := meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryResolvedReason, cond.Reason)

	data := f.bootstrapData(t)
	assert.Contains(t, data, "image: factory.example.test/metal-installer/sid-siderolabs/nvme-cli:v1.14.2", "machine.install.image must be rendered")

	secret := &corev1.Secret{}
	require.NoError(t, f.client.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: cfg.Status.DataSecretName}, secret))
	wantHash, err := bootstrapv1beta1.InPlaceConfigHash(cfg.Spec, "1.34.0", cfg.Status.ImageFactory.InstallerImage)
	require.NoError(t, err)
	assert.Equal(t, wantHash, secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation], "the hash must include the resolved image")
}

func TestReconcileWithoutImageFactoryBlockRendersNoImage(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.14"}, nil)

	require.NoError(t, f.reconcile(t))

	cfg := f.config(t)
	assert.Nil(t, cfg.Status.ImageFactory)
	assert.Nil(t, meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition))
	assert.False(t, strings.Contains(f.bootstrapData(t), "metal-installer"), "no image must be injected without the block")
}

func TestReconcileFailsClosedWhenTheFactoryIsDown(t *testing.T) {
	t.Parallel()

	factory := &fakeFactory{err: &imagefactory.FactoryError{Err: errors.New("dial tcp: connection refused")}}
	f := newImageFactoryFixture(t, specWith("v1.14", "siderolabs/nvme-cli"), factory)

	require.Error(t, f.reconcile(t))

	cfg := f.config(t)
	assert.Empty(t, cfg.Status.DataSecretName, "no bootstrap data may be written without the image")
	assert.False(t, ptr.Deref(cfg.Status.Initialization.DataSecretCreated, false))

	cond := meta.FindStatusCondition(cfg.Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryUnavailableReason, cond.Reason)
	assert.Contains(t, cond.Message, "connection refused")
}

func TestReconcileReportsUnknownExtensions(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, specWith("v1.14.2", "siderolabs/nope"), newFakeFactory())

	require.Error(t, f.reconcile(t))

	cond := meta.FindStatusCondition(f.config(t).Status.Conditions, bootstrapv1beta1.ImageFactoryResolvedCondition)
	require.NotNil(t, cond)
	assert.Equal(t, bootstrapv1beta1.ImageFactoryUnknownExtensionReason, cond.Reason)
	assert.Contains(t, cond.Message, "siderolabs/nope")
}

func TestReconcileRequiresAClientWhenTheBlockIsSet(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, specWith("v1.14.2", "siderolabs/nvme-cli"), nil)

	err := f.reconcile(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image-factory-url")
}
