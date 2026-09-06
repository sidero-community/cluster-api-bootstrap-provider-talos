// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	"sigs.k8s.io/cluster-api/feature"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/inplace"
)

const (
	// nodeLabelPatch is a spec change whose effect is visible in the rendered machine
	// configuration, so a test can tell a re-render from a stale secret.
	nodeLabelPatch = "machine:\n  nodeLabels:\n    rendered-from: patched-spec\n"

	testNamespace   = "default"
	testClusterName = "test"
	testPoolName    = "pool-1"
)

// appliedConfig is one ApplyConfiguration call against a node, identified by the endpoint the
// client was opened for.
type appliedConfig struct {
	endpoint string
	data     string
}

// nodeRecorder stands in for the Talos machine API across a whole pool.
//
// It records the applies in call order so a test can assert both that each node was visited
// exactly once and that the pool was walked one node at a time.
type nodeRecorder struct {
	mu sync.Mutex

	applied  []appliedConfig
	upgrades []string

	open    int
	maxOpen int

	// applyErr fails ApplyConfig for the node reached at the given endpoint.
	applyErr map[string]error

	// connectErr fails opening a client for the given endpoint.
	connectErr map[string]error
}

func (n *nodeRecorder) factory() inplace.NodeClientFactory {
	return func(_ context.Context, _ types.NamespacedName, endpoints []string) (inplace.NodeClient, error) {
		n.mu.Lock()
		defer n.mu.Unlock()

		endpoint := endpoints[0]

		if err := n.connectErr[endpoint]; err != nil {
			return nil, err
		}

		n.open++
		if n.open > n.maxOpen {
			n.maxOpen = n.open
		}

		return &recordingNode{recorder: n, endpoint: endpoint}, nil
	}
}

func (n *nodeRecorder) endpoints() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := make([]string, 0, len(n.applied))
	for _, a := range n.applied {
		out = append(out, a.endpoint)
	}

	return out
}

type recordingNode struct {
	recorder *nodeRecorder
	endpoint string
}

func (r *recordingNode) Version(context.Context) (string, error) { return "v1.11.0", nil }

func (r *recordingNode) ApplyConfig(_ context.Context, data []byte) error {
	r.recorder.mu.Lock()
	defer r.recorder.mu.Unlock()

	if err := r.recorder.applyErr[r.endpoint]; err != nil {
		return err
	}

	r.recorder.applied = append(r.recorder.applied, appliedConfig{endpoint: r.endpoint, data: string(data)})

	return nil
}

func (r *recordingNode) Upgrade(_ context.Context, image string) error {
	r.recorder.mu.Lock()
	defer r.recorder.mu.Unlock()

	r.recorder.upgrades = append(r.recorder.upgrades, image)

	return nil
}

func (r *recordingNode) Close() error {
	r.recorder.mu.Lock()
	defer r.recorder.mu.Unlock()

	r.recorder.open--

	return nil
}

// enableMachinePools turns on the Cluster API MachinePool feature gate, without which
// bsutil.GetConfigOwner refuses to resolve a MachinePool owner at all.
func enableMachinePools(t *testing.T) {
	t.Helper()

	require.NoError(t, feature.MutableGates.Set("MachinePool=true"))
	t.Cleanup(func() {
		require.NoError(t, feature.MutableGates.Set("MachinePool=false"))
	})
}

type fixtureOptions struct {
	ownerKind string

	// poolMachines are the addresses of the Machines the infrastructure provider created for
	// the pool, keyed by machine name.
	poolMachines map[string]string

	// otherMachines are Machines in the same cluster that belong to something else, and so must
	// never be touched.
	otherMachines map[string]string

	enabled bool
}

type fixture struct {
	reconciler *TalosConfigReconciler
	request    ctrl.Request
	nodes      *nodeRecorder
}

// newFixture builds a reconciler over a cluster whose infrastructure is provisioned, an owner
// of the given kind, and a TalosConfig owned by it.
func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, capiv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	ownerName := "machine-1"
	if opts.ownerKind == "MachinePool" {
		ownerName = testPoolName
	}

	objects := []client.Object{
		&capiv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: testClusterName, Namespace: testNamespace},
			Spec: capiv1.ClusterSpec{
				ControlPlaneEndpoint: capiv1.APIEndpoint{Host: "1.2.3.4", Port: 6443},
			},
			Status: capiv1.ClusterStatus{
				Initialization: capiv1.ClusterInitializationStatus{
					InfrastructureProvisioned: ptr.To(true),
				},
			},
		},
	}

	switch opts.ownerKind {
	case "MachinePool":
		objects = append(objects, &capiv1.MachinePool{
			ObjectMeta: metav1.ObjectMeta{Name: ownerName, Namespace: testNamespace, UID: "owner-uid"},
			Spec: capiv1.MachinePoolSpec{
				ClusterName: testClusterName,
				Template: capiv1.MachineTemplateSpec{
					Spec: capiv1.MachineSpec{ClusterName: testClusterName, Version: "v1.34.0"},
				},
			},
		})
	default:
		objects = append(objects, &capiv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: ownerName, Namespace: testNamespace, UID: "owner-uid"},
			Spec:       capiv1.MachineSpec{ClusterName: testClusterName, Version: "v1.34.0"},
		})
	}

	objects = append(objects, &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ownerName,
			Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: capiv1.GroupVersion.String(),
				Kind:       opts.ownerKind,
				Name:       ownerName,
				UID:        "owner-uid",
			}},
		},
		Spec: bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.11"},
	})

	for name, address := range opts.poolMachines {
		objects = append(objects, poolMachine(name, address, map[string]string{
			// Exactly the labels the Cluster API machinepool controller stamps on a pool Machine,
			// see internal/controllers/machinepool/machinepool_controller_phases.go.
			capiv1.MachinePoolNameLabel: testPoolName,
			capiv1.ClusterNameLabel:     testClusterName,
		}))
	}

	for name, address := range opts.otherMachines {
		objects = append(objects, poolMachine(name, address, map[string]string{
			capiv1.ClusterNameLabel: testClusterName,
		}))
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&bootstrapv1beta1.TalosConfig{}).
		WithObjects(objects...).
		Build()

	nodes := &nodeRecorder{applyErr: map[string]error{}, connectErr: map[string]error{}}

	return &fixture{
		reconciler: &TalosConfigReconciler{
			Client:                    c,
			Log:                       ctrl.Log.WithName("test"),
			Scheme:                    scheme,
			MachinePoolInPlaceUpdates: opts.enabled,
			NodeClientFactory:         nodes.factory(),
		},
		request: ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: ownerName}},
		nodes:   nodes,
	}
}

// poolMachine builds a Machine the way the Cluster API machinepool controller does. An empty
// address stands for a member whose infrastructure has not reported one yet.
func poolMachine(name, address string, labels map[string]string) *capiv1.Machine {
	machine := &capiv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels},
		Spec:       capiv1.MachineSpec{ClusterName: testClusterName},
	}

	if address != "" {
		machine.Status.Addresses = []capiv1.MachineAddress{
			{Type: capiv1.MachineInternalIP, Address: address},
		}
	}

	return machine
}

func (f *fixture) reconcile(t *testing.T) ctrl.Result {
	t.Helper()

	result, err := f.reconciler.Reconcile(context.Background(), f.request)
	require.NoError(t, err)

	return result
}

// provision runs the reconcile that renders the bootstrap data for the first time. It is the
// path a config takes before it is ready, and it never talks to a node.
func (f *fixture) provision(t *testing.T) {
	t.Helper()

	f.reconcile(t)

	require.Empty(t, f.nodes.applied, "provisioning must not touch any node")
}

func (f *fixture) reconcileExpectingError(t *testing.T) error {
	t.Helper()

	_, err := f.reconciler.Reconcile(context.Background(), f.request)
	require.Error(t, err)

	return err
}

func (f *fixture) secret(t *testing.T) *corev1.Secret {
	t.Helper()

	secret := &corev1.Secret{}
	require.NoError(t, f.reconciler.Client.Get(context.Background(), types.NamespacedName{
		Namespace: f.request.Namespace,
		Name:      f.request.Name + "-bootstrap-data",
	}, secret))

	return secret
}

func (f *fixture) config(t *testing.T) *bootstrapv1beta1.TalosConfig {
	t.Helper()

	config := &bootstrapv1beta1.TalosConfig{}
	require.NoError(t, f.reconciler.Client.Get(context.Background(), f.request.NamespacedName, config))

	return config
}

func (f *fixture) machine(t *testing.T, name string) *capiv1.Machine {
	t.Helper()

	machine := &capiv1.Machine{}
	require.NoError(t, f.reconciler.Client.Get(context.Background(), types.NamespacedName{
		Namespace: testNamespace, Name: name,
	}, machine))

	return machine
}

// patchSpec adds a strategic patch to the TalosConfig, standing in for the ClusterClass change
// the topology controller writes onto a topology-owned config.
func (f *fixture) patchSpec(t *testing.T) {
	t.Helper()

	config := f.config(t)
	config.Spec.StrategicPatches = []string{nodeLabelPatch}
	require.NoError(t, f.reconciler.Client.Update(context.Background(), config))
}

func (f *fixture) poolCondition(t *testing.T) *metav1.Condition {
	t.Helper()

	return meta.FindStatusCondition(f.config(t).Status.Conditions, bootstrapv1beta1.MachinePoolInPlaceUpdateCondition)
}

// (a) A pool keeps a single bootstrap data secret for every instance it will ever create, so a
// spec change has to be rendered into that same secret rather than a new one.
func TestMachinePoolInPlace_RerendersSecretOnSpecChange(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{ownerKind: "MachinePool", enabled: true})

	f.provision(t)

	initial := f.secret(t)
	require.NotContains(t, string(initial.Data["value"]), "rendered-from")

	f.patchSpec(t)
	f.reconcile(t)

	updated := f.secret(t)
	assert.Equal(t, initial.Name, updated.Name, "the pool's bootstrap reference must stay valid")
	assert.Contains(t, string(updated.Data["value"]), "rendered-from: patched-spec")
	assert.NotEqual(t,
		initial.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation],
		updated.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation],
		"the stamped hash is what gates the next re-render")
}

// The re-render gate has to agree with the hash writeBootstrapData stamps, or every reconcile
// would rewrite the secret and re-apply the configuration to every node in the pool.
func TestMachinePoolInPlace_DoesNotRerenderUnchangedSpec(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1"},
		enabled:      true,
	})

	f.provision(t)

	initial := f.secret(t)

	f.reconcile(t)
	f.reconcile(t)

	assert.Equal(t, initial.ResourceVersion, f.secret(t).ResourceVersion)
	assert.Len(t, f.nodes.applied, 1, "a converged node must not be applied to again")
}

// (b) Every running member is brought to the re-rendered configuration, one at a time, and a
// member that already carries the hash is skipped on the next pass.
func TestMachinePoolInPlace_AppliesToEachMemberOnceSequentially(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind: "MachinePool",
		poolMachines: map[string]string{
			"pool-1-a": "10.0.0.1",
			"pool-1-b": "10.0.0.2",
			"pool-1-c": "10.0.0.3",
		},
		otherMachines: map[string]string{"standalone": "10.0.9.9"},
		enabled:       true,
	})

	f.provision(t)

	result := f.reconcile(t)

	require.Len(t, f.nodes.applied, 3)
	assert.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, f.nodes.endpoints(),
		"members are walked in a deterministic order")
	assert.Equal(t, 1, f.nodes.maxOpen, "the pool is updated one node at a time")
	assert.Empty(t, f.nodes.upgrades, "pool members are never upgraded, only reconfigured")
	assert.Zero(t, result.RequeueAfter, "a fully converged pool has nothing to come back for")

	// The bytes pushed to the nodes are the bytes in the secret, not a second rendering.
	secret := string(f.secret(t).Data["value"])
	for _, applied := range f.nodes.applied {
		assert.Equal(t, secret, applied.data)
	}

	hash := f.secret(t).Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation]
	for _, name := range []string{"pool-1-a", "pool-1-b", "pool-1-c"} {
		assert.Equal(t, hash, f.machine(t, name).Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation], name)
	}

	assert.Empty(t, f.machine(t, "standalone").Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation],
		"a machine outside the pool must not be touched")

	condition := f.poolCondition(t)
	if assert.NotNil(t, condition) {
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		assert.Contains(t, condition.Message, "3 of 3")
	}

	// Requeueing must not re-apply to a converged node.
	f.reconcile(t)
	assert.Len(t, f.nodes.applied, 3)

	// A spec change re-renders and brings every member to the new configuration once more.
	f.patchSpec(t)
	f.reconcile(t)
	assert.Len(t, f.nodes.applied, 6)
}

// (c) The pool is walked strictly in order and stops at the first failure, so a bad
// configuration cannot be pushed to the whole pool before anyone notices.
func TestMachinePoolInPlace_StopsAtFirstFailure(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind: "MachinePool",
		poolMachines: map[string]string{
			"pool-1-a": "10.0.0.1",
			"pool-1-b": "10.0.0.2",
			"pool-1-c": "10.0.0.3",
		},
		enabled: true,
	})

	f.provision(t)

	f.nodes.applyErr["10.0.0.2"] = errors.New("node rejected the configuration")

	err := f.reconcileExpectingError(t)
	assert.Contains(t, err.Error(), "pool-1-b")

	assert.Equal(t, []string{"10.0.0.1"}, f.nodes.endpoints(),
		"the members after the failure must not be applied to")
	assert.Empty(t, f.machine(t, "pool-1-b").Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation],
		"a failed node must not be recorded as converged")

	condition := f.poolCondition(t)
	if assert.NotNil(t, condition) {
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, bootstrapv1beta1.MachinePoolInPlaceUpdateFailedReason, condition.Reason)
	}
}

// A member whose infrastructure has not published an address yet cannot be reached; it is left
// for a later pass rather than failing the whole pool.
func TestMachinePoolInPlace_RequeuesForMembersWithoutAddresses(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1", "pool-1-b": ""},
		enabled:      true,
	})

	f.provision(t)

	result := f.reconcile(t)

	assert.Len(t, f.nodes.applied, 1)
	assert.NotZero(t, result.RequeueAfter, "the pool has not converged yet")

	condition := f.poolCondition(t)
	if assert.NotNil(t, condition) {
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
		assert.Equal(t, bootstrapv1beta1.MachinePoolInPlaceUpdateInProgressReason, condition.Reason)
		assert.Contains(t, condition.Message, "1 of 2")
	}
}

// An infrastructure provider that does not publish status.infrastructureMachineKind gets no
// pool Machines, and there is no other way to find the nodes. Say so rather than guessing.
func TestMachinePoolInPlace_ReportsUnknownWithoutPoolMachines(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{ownerKind: "MachinePool", enabled: true})

	f.provision(t)

	result := f.reconcile(t)
	assert.Zero(t, result.RequeueAfter)
	assert.Empty(t, f.nodes.applied)

	condition := f.poolCondition(t)
	if assert.NotNil(t, condition) {
		assert.Equal(t, metav1.ConditionUnknown, condition.Status)
		assert.Equal(t, bootstrapv1beta1.MachinePoolInPlaceUpdateMachinesUnavailableReason, condition.Reason)
	}
}

// Carrying on past the ready fast path must not cost a ready MachinePool config the status a
// ready Machine config gets.
func TestMachinePoolInPlace_KeepsReadyStatus(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1"},
		enabled:      true,
	})

	f.reconcile(t)
	f.reconcile(t)

	config := f.config(t)

	assert.True(t, ptr.Deref(config.Status.Initialization.DataSecretCreated, false))
	assert.Equal(t, f.request.Name+"-bootstrap-data", config.Status.DataSecretName)

	for _, conditionType := range []string{
		bootstrapv1beta1.DataSecretAvailableCondition,
		bootstrapv1beta1.ClientConfigAvailableCondition,
	} {
		condition := meta.FindStatusCondition(config.Status.Conditions, conditionType)
		if assert.NotNil(t, condition, conditionType) {
			assert.Equal(t, metav1.ConditionTrue, condition.Status, conditionType)
		}
	}
}

// (d) Nothing re-reads a Machine's bootstrap data after it has booted, so it stays frozen; a
// running Machine is updated through the Cluster API in-place flow or replaced.
func TestMachinePoolInPlace_MachineOwnedConfigIsUntouched(t *testing.T) {
	f := newFixture(t, fixtureOptions{ownerKind: "Machine", enabled: true})

	f.reconcile(t)

	initial := f.secret(t)

	f.patchSpec(t)
	f.reconcile(t)

	updated := f.secret(t)
	assert.Equal(t, string(initial.Data["value"]), string(updated.Data["value"]))
	assert.Equal(t, initial.ResourceVersion, updated.ResourceVersion)
	assert.Empty(t, f.nodes.applied)
	assert.Nil(t, f.poolCondition(t))
}

// (e) With the flag off the pool behaves exactly as it did before: the secret is frozen once
// rendered and nothing talks to the nodes.
func TestMachinePoolInPlace_DisabledKeepsPreviousBehaviour(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1"},
		enabled:      false,
	})

	f.reconcile(t)

	initial := f.secret(t)

	f.patchSpec(t)
	f.reconcile(t)

	updated := f.secret(t)
	assert.Equal(t, string(initial.Data["value"]), string(updated.Data["value"]))
	assert.Equal(t, initial.ResourceVersion, updated.ResourceVersion)
	assert.Empty(t, f.nodes.applied)
	assert.Nil(t, f.poolCondition(t))
}

// (f) Initial provisioning is untouched: the first reconcile just renders the secret, with no
// nodes to talk to and nothing to converge.
func TestMachinePoolInPlace_InitialProvisioningIsUnchanged(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1"},
		enabled:      true,
	})

	config := f.config(t)
	require.False(t, ptr.Deref(config.Status.Initialization.DataSecretCreated, false))

	result := f.reconcile(t)

	assert.Zero(t, result.RequeueAfter)
	assert.Empty(t, f.nodes.applied, "a pool being provisioned has no running members to update")

	secret := f.secret(t)
	assert.NotEmpty(t, secret.Data["value"])
	assert.NotEmpty(t, secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation])
	assert.Nil(t, f.poolCondition(t))

	config = f.config(t)
	assert.True(t, ptr.Deref(config.Status.Initialization.DataSecretCreated, false))
}

// Failing to reach a node is a transient condition, not a reason to record it as converged.
func TestMachinePoolInPlace_ConnectFailureIsReported(t *testing.T) {
	enableMachinePools(t)

	f := newFixture(t, fixtureOptions{
		ownerKind:    "MachinePool",
		poolMachines: map[string]string{"pool-1-a": "10.0.0.1"},
		enabled:      true,
	})

	f.provision(t)

	f.nodes.connectErr["10.0.0.1"] = fmt.Errorf("dial tcp 10.0.0.1:50000: connection refused")

	err := f.reconcileExpectingError(t)
	assert.Contains(t, err.Error(), "pool-1-a")

	assert.Empty(t, f.machine(t, "pool-1-a").Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation])

	condition := f.poolCondition(t)
	if assert.NotNil(t, condition) {
		assert.Equal(t, metav1.ConditionFalse, condition.Status)
	}
}
