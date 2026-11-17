// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

// gateMachine describes one Machine of the fixture cluster and its TalosConfig.
type gateMachine struct {
	name string
	// generateType is the TalosConfig's; empty means the Machine has no TalosConfig at all.
	generateType string
	restrictJoin bool
	node         string
	nodeReady    bool
	deleting     bool
	cluster      string
}

func (m gateMachine) uid() types.UID { return types.UID("uid-" + m.name) }

func (m gateMachine) objects() []client.Object {
	clusterName := m.cluster
	if clusterName == "" {
		clusterName = testClusterName
	}

	machine := &capiv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.name, Namespace: testNamespace, UID: m.uid(),
			Labels: map[string]string{capiv1.ClusterNameLabel: clusterName},
		},
		Spec: capiv1.MachineSpec{ClusterName: clusterName},
	}

	if m.deleting {
		machine.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		machine.Finalizers = []string{"test/hold"}
	}

	if m.node != "" {
		machine.Status.NodeRef = capiv1.MachineNodeReference{Name: m.node}
	}

	if m.nodeReady {
		machine.Status.Conditions = []metav1.Condition{{
			Type: capiv1.MachineNodeReadyCondition, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now(),
		}}
	}

	if m.generateType == "" {
		return []client.Object{machine}
	}

	machine.Spec.Bootstrap.ConfigRef = capiv1.ContractVersionedObjectReference{
		APIGroup: bootstrapv1beta1.GroupVersion.Group, Kind: "TalosConfig", Name: m.name,
	}

	return []client.Object{machine, &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: m.name, Namespace: testNamespace,
			Labels: map[string]string{capiv1.ClusterNameLabel: clusterName},
		},
		Spec: bootstrapv1beta1.TalosConfigSpec{GenerateType: m.generateType, RestrictJoin: m.restrictJoin},
	}}
}

type joinGateFixture struct {
	management  client.Client
	workload    client.Client
	reconciler  *JoinGateReconciler
	bundle      *secrets.Bundle
	connections int
}

type gateOption func(*capiv1.Cluster, *[]client.Object, *[]client.Object)

// gateInstalled records that an earlier reconcile put a gate in the workload cluster.
func gateInstalled(cluster *capiv1.Cluster, management, _ *[]client.Object) {
	*management = append(*management, joinGateMarker(cluster, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name + joinGateMarkerSuffix}))
}

func gateWorkloadObjects(objects ...client.Object) gateOption {
	return func(_ *capiv1.Cluster, _, workload *[]client.Object) { *workload = append(*workload, objects...) }
}

func gateCluster(mutate func(*capiv1.Cluster)) gateOption {
	return func(cluster *capiv1.Cluster, _, _ *[]client.Object) { mutate(cluster) }
}

func newJoinGateFixture(t *testing.T, machines []gateMachine, options ...gateOption) *joinGateFixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, admissionregistrationv1.AddToScheme(scheme))
	require.NoError(t, capiv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	bundle, err := secrets.NewBundle(secrets.NewClock(), config.TalosVersionCurrent)
	require.NoError(t, err)

	marshalled, err := yaml.Marshal(bundle)
	require.NoError(t, err)

	cluster := &capiv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: testClusterName, Namespace: testNamespace, UID: "cluster-uid"},
		Status:     capiv1.ClusterStatus{Initialization: capiv1.ClusterInitializationStatus{ControlPlaneInitialized: new(true)}},
	}

	management := []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testClusterName + "-talos", Namespace: testNamespace},
			Data:       map[string][]byte{"bundle": marshalled},
		},
	}

	for _, machine := range machines {
		management = append(management, machine.objects()...)
	}

	var workload []client.Object

	for _, option := range options {
		option(cluster, &management, &workload)
	}

	management = append(management, cluster)

	f := &joinGateFixture{
		management: fake.NewClientBuilder().WithScheme(scheme).WithObjects(management...).
			WithStatusSubresource(&capiv1.Machine{}, &capiv1.Cluster{}).Build(),
		workload: fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload...).Build(),
		bundle:   bundle,
	}

	f.reconciler = &JoinGateReconciler{
		Client: f.management,
		Log:    ctrl.Log.WithName("test"),
		WorkloadClient: func(context.Context, client.ObjectKey) (client.Client, error) {
			f.connections++

			return f.workload, nil
		},
	}

	return f
}

func (f *joinGateFixture) reconcile(t *testing.T) ctrl.Result {
	t.Helper()

	result, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testClusterName}})
	require.NoError(t, err)

	return result
}

func (f *joinGateFixture) allowlist(t *testing.T) *corev1.ConfigMap {
	t.Helper()

	allowlist := &corev1.ConfigMap{}
	require.NoError(t, f.workload.Get(context.Background(), types.NamespacedName{Namespace: "kube-system", Name: JoinAllowlistName}, allowlist))

	return allowlist
}

func (f *joinGateFixture) validationActions(t *testing.T) []admissionregistrationv1.ValidationAction {
	t.Helper()

	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	require.NoError(t, f.workload.Get(context.Background(), types.NamespacedName{Name: JoinPolicyName}, binding))

	return binding.Spec.ValidationActions
}

// bootstrapTokens returns the names of the Secrets in the workload cluster's kube-system.
func (f *joinGateFixture) bootstrapTokens(t *testing.T) []string {
	t.Helper()

	list := &corev1.SecretList{}
	require.NoError(t, f.workload.List(context.Background(), list, client.InNamespace("kube-system")))

	names := []string{}
	for _, secret := range list.Items {
		names = append(names, secret.Name)
	}

	return names
}

func (f *joinGateFixture) tokenOf(t *testing.T, machine gateMachine) (id, secret string) {
	t.Helper()

	id, secret, err := joinToken(f.bundle, string(machine.uid()))
	require.NoError(t, err)

	return id, secret
}

func (f *joinGateFixture) hasMarker(t *testing.T) bool {
	t.Helper()

	err := f.management.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: testClusterName + joinGateMarkerSuffix}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		return false
	}

	require.NoError(t, err)

	return true
}

var (
	restrictedControlPlane = gateMachine{name: "cp-0", generateType: "controlplane", restrictJoin: true, node: "cp-0", nodeReady: true}
	joiningWorker          = gateMachine{name: "worker-0", generateType: "worker", restrictJoin: true}
)

func TestJoinGateLeavesAClusterWithoutRestrictionsAlone(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{
		{name: "cp-0", generateType: "controlplane", node: "cp-0", nodeReady: true},
		{name: "worker-0", generateType: "worker"},
	})

	assert.Zero(t, f.reconcile(t))
	assert.Zero(t, f.connections, "a cluster that never asked for the gate must not be connected to")
	assert.False(t, f.hasMarker(t))
}

func TestJoinGateAdmitsLivingMachinesOnly(t *testing.T) {
	t.Parallel()

	leaving := gateMachine{name: "worker-old", generateType: "worker", restrictJoin: true, node: "worker-old", deleting: true}
	foreign := gateMachine{name: "elsewhere", generateType: "worker", restrictJoin: true, cluster: "another"}

	f := newJoinGateFixture(t, []gateMachine{restrictedControlPlane, joiningWorker, leaving, foreign})

	result := f.reconcile(t)
	assert.Equal(t, joinGateResync, result.RequeueAfter, "the workload cluster's side is repaired periodically")

	allowlist := f.allowlist(t)
	assert.Equal(t, map[string]string{
		"id.uid-cp-0":     "Machine cp-0",
		"id.uid-worker-0": "Machine worker-0",
	}, allowlist.Data)
	assert.Equal(t, "Deny", allowlist.Annotations[JoinEnforcementAnnotation])
	assert.Equal(t, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}, f.validationActions(t))

	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	require.NoError(t, f.workload.Get(context.Background(), types.NamespacedName{Name: JoinPolicyName}, policy))
	assert.Equal(t, joinPolicySpec(), policy.Spec)

	assert.True(t, f.hasMarker(t))
}

func TestJoinGateGivesAJoiningWorkerItsToken(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{restrictedControlPlane, joiningWorker})
	f.reconcile(t)

	id, tokenSecret := f.tokenOf(t, joiningWorker)

	// Only the worker: a control plane node joins with the cluster-wide token.
	require.Equal(t, []string{"bootstrap-token-" + id}, f.bootstrapTokens(t))

	secret := &corev1.Secret{}
	require.NoError(t, f.workload.Get(context.Background(), types.NamespacedName{Namespace: "kube-system", Name: "bootstrap-token-" + id}, secret))

	assert.Equal(t, corev1.SecretTypeBootstrapToken, secret.Type)
	assert.Equal(t, "uid-worker-0", secret.Labels[JoinTokenLabel])
	assert.Equal(t, id, string(secret.Data["token-id"]))
	assert.Equal(t, tokenSecret, string(secret.Data["token-secret"]))
	assert.Equal(t, "true", string(secret.Data["usage-bootstrap-authentication"]))
	assert.Equal(t, "system:bootstrappers:nodes", string(secret.Data["auth-extra-groups"]))
}

func TestJoinGateTakesTheTokenBackOnceTheNodeIsReady(t *testing.T) {
	t.Parallel()

	joined := joiningWorker
	joined.node, joined.nodeReady = "worker-0", true

	f := newJoinGateFixture(t, []gateMachine{restrictedControlPlane, joiningWorker})
	f.reconcile(t)
	require.Len(t, f.bootstrapTokens(t), 1)

	machine := joined.objects()[0].(*capiv1.Machine) //nolint:forcetypeassert // the first object is the Machine
	require.NoError(t, f.management.Status().Update(context.Background(), withResourceVersion(t, f.management, machine)))

	f.reconcile(t)

	assert.Empty(t, f.bootstrapTokens(t))
	assert.Contains(t, f.allowlist(t).Data, "id.uid-worker-0", "the node may still have to register again")
}

func TestJoinGateForgetsADeletedMachine(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{restrictedControlPlane, joiningWorker})
	f.reconcile(t)
	require.Len(t, f.bootstrapTokens(t), 1)

	machine := &capiv1.Machine{}
	require.NoError(t, f.management.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "worker-0"}, machine))
	require.NoError(t, f.management.Delete(context.Background(), machine))

	f.reconcile(t)

	assert.Empty(t, f.bootstrapTokens(t), "the configuration of a deleted Machine must stop working")
	assert.Equal(t, map[string]string{"id.uid-cp-0": "Machine cp-0"}, f.allowlist(t).Data)
}

func TestJoinGateKnowsUnrestrictedMachinesByTheirNode(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{
		{name: "cp-0", generateType: "controlplane", node: "talos-cp-0", nodeReady: true},
		{name: "foreign", node: "foreign-node", nodeReady: true},
		joiningWorker,
	})
	f.reconcile(t)

	allowlist := f.allowlist(t)
	assert.Equal(t, map[string]string{
		"name.talos-cp-0":   "Machine cp-0",
		"name.foreign-node": "Machine foreign",
		"id.uid-worker-0":   "Machine worker-0",
	}, allowlist.Data)
	assert.Equal(t, "Deny", allowlist.Annotations[JoinEnforcementAnnotation])
}

func TestJoinGateOnlyWarnsWhileAMachineCannotBeDescribed(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{
		restrictedControlPlane,
		joiningWorker,
		{name: "cp-1", generateType: "controlplane"},
	})
	f.reconcile(t)

	allowlist := f.allowlist(t)
	assert.Equal(t, "Warn", allowlist.Annotations[JoinEnforcementAnnotation])
	assert.Equal(t, "Machine cp-1", allowlist.Annotations[JoinUncoveredAnnotation])
	assert.Equal(t,
		[]admissionregistrationv1.ValidationAction{admissionregistrationv1.Warn, admissionregistrationv1.Audit},
		f.validationActions(t),
		"refusing registrations would lock out a machine that has neither a join ID nor a Node",
	)
}

func TestJoinGateIsRemovedWhenNothingRestrictsJoining(t *testing.T) {
	t.Parallel()

	clusterToken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "bootstrap-token-shared"},
		Type:       corev1.SecretTypeBootstrapToken,
	}
	staleToken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "bootstrap-token-stale1", Labels: map[string]string{JoinTokenLabel: "gone"}},
		Type:       corev1.SecretTypeBootstrapToken,
	}

	f := newJoinGateFixture(t,
		[]gateMachine{{name: "cp-0", generateType: "controlplane", node: "cp-0", nodeReady: true}},
		gateInstalled,
		gateWorkloadObjects(
			clusterToken, staleToken,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: JoinAllowlistName}},
			&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
			&admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
		),
	)

	assert.Zero(t, f.reconcile(t))

	for _, object := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: JoinAllowlistName}},
		&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
		&admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: JoinPolicyName}},
	} {
		err := f.workload.Get(context.Background(), client.ObjectKeyFromObject(object), object)
		assert.True(t, apierrors.IsNotFound(err), "%T must be removed, got %v", object, err)
	}

	assert.Equal(t, []string{"bootstrap-token-shared"}, f.bootstrapTokens(t), "only the gate's own tokens are its to remove")
	assert.False(t, f.hasMarker(t))

	// With the marker gone the cluster is left alone again.
	f.reconcile(t)
	assert.Equal(t, 1, f.connections)
}

func TestJoinGateWaitsForTheControlPlane(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{joiningWorker}, gateCluster(func(cluster *capiv1.Cluster) {
		cluster.Status.Initialization.ControlPlaneInitialized = nil
	}))

	assert.Zero(t, f.reconcile(t))
	assert.Zero(t, f.connections)
}

func TestJoinGateLeavesAPausedClusterAlone(t *testing.T) {
	t.Parallel()

	f := newJoinGateFixture(t, []gateMachine{joiningWorker}, gateCluster(func(cluster *capiv1.Cluster) {
		cluster.Spec.Paused = new(true)
	}))

	assert.Zero(t, f.reconcile(t))
	assert.Zero(t, f.connections)
}

// withResourceVersion returns object carrying the resource version the client holds for it, so
// it can be written back.
func withResourceVersion[T client.Object](t *testing.T, c client.Client, object T) T {
	t.Helper()

	current := object.DeepCopyObject().(T) //nolint:forcetypeassert // a copy has the type of its source
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(object), current))

	object.SetResourceVersion(current.GetResourceVersion())

	return object
}
