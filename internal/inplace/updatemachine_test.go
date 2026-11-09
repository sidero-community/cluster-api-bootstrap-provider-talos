// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

// machineConfigWithImage renders a minimal but valid Talos machine configuration pinned to
// the given installer image.
func machineConfigWithImage(image string) string {
	return fmt.Sprintf(`version: v1alpha1
debug: false
machine:
  type: worker
  token: aaaaaa.bbbbbbbbbbbbbbbb
  ca:
    crt: ""
    key: ""
  install:
    image: %s
    disk: /dev/sda
cluster:
  id: dGVzdA==
  secret: dGVzdA==
  controlPlane:
    endpoint: https://192.168.1.1:6443
  token: aaaaaa.bbbbbbbbbbbbbbbb
  ca:
    crt: ""
    key: ""
`, image)
}

// fakeNode records what the update state machine did to a node. The tracker drives Upgrade
// from a goroutine, so the fields it touches are guarded.
type fakeNode struct {
	mu sync.Mutex

	version string

	applied        [][]byte
	upgrades       []string
	legacyUpgrades []string

	versionErr error
	upgradeErr error

	// blockUpgrades makes Upgrade wait for its context to end, as a real install stream
	// would, and record why it ended.
	blockUpgrades bool
	upgradeCtxErr error

	apiServer      APIServerState
	apiServerErr   error
	apiServerCalls int
}

func (f *fakeNode) Version(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.version, f.versionErr
}

func (f *fakeNode) setVersion(version string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.version = version
}

// upgradesSeen returns a copy of the lifecycle upgrades requested so far.
func (f *fakeNode) upgradesSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.upgrades...)
}

func (f *fakeNode) ApplyConfig(_ context.Context, data []byte) error {
	f.applied = append(f.applied, data)

	return nil
}

func (f *fakeNode) Upgrade(ctx context.Context, image string) error {
	f.mu.Lock()
	f.upgrades = append(f.upgrades, image)
	block := f.blockUpgrades
	f.mu.Unlock()

	if block {
		<-ctx.Done()

		f.mu.Lock()
		f.upgradeCtxErr = ctx.Err()
		f.mu.Unlock()

		return ctx.Err()
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return f.upgradeErr
}

// upgradeContextError returns why a blocked upgrade's context ended, if it has.
func (f *fakeNode) upgradeContextError() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.upgradeCtxErr
}

func (f *fakeNode) APIServer(context.Context) (APIServerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.apiServerCalls++

	return f.apiServer, f.apiServerErr
}

func (f *fakeNode) setAPIServer(state APIServerState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.apiServer = state
}

func (f *fakeNode) LegacyUpgrade(_ context.Context, image string) error {
	f.legacyUpgrades = append(f.legacyUpgrades, image)

	return nil
}

func (f *fakeNode) Close() error { return nil }

type updateFixture struct {
	handler *Handler
	node    *fakeNode
	request *runtimehooksv1.UpdateMachineRequest
}

// newUpdateFixture wires a handler against a fake node and a fake management cluster
// holding a bootstrap data secret stamped with secretHash.
func newUpdateFixture(t *testing.T, runningVersion, configImage, secretHash, resolvedImage string) *updateFixture {
	t.Helper()

	const (
		namespace = "default"
		cluster   = "test-cluster"
	)

	desiredSpec := map[string]any{"generateType": "worker", "talosVersion": "v1.13"}

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, clusterv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        "machine-1-bootstrap-data",
			Annotations: map[string]string{bootstrapv1beta1.InPlaceConfigHashAnnotation: secretHash},
		},
		Data: map[string][]byte{"value": []byte(machineConfigWithImage(configImage))},
	}

	node := &fakeNode{version: runningVersion}

	// The addresses live only on the object in the API server. Cluster API strips status when it
	// builds an UpdateMachine request, so a handler that trusts the request object never sees
	// them and waits forever.
	liveMachine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "machine-1"},
		Status: clusterv1.MachineStatus{
			Addresses: []clusterv1.MachineAddress{{Type: clusterv1.MachineInternalIP, Address: "192.168.1.10"}},
		},
	}

	liveConfig := &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "machine-1"},
		Spec:       bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"},
	}
	if resolvedImage != "" {
		liveConfig.Status.ImageFactory = &bootstrapv1beta1.ImageFactoryStatus{InstallerImage: resolvedImage}
	}

	handler := NewHandler(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, liveMachine, liveConfig).Build(),
		func(context.Context, types.NamespacedName, []string) (NodeClient, error) { return node, nil },
	)

	machine := clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "machine-1"},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster,
			Version:     "v1.34.0",
			Bootstrap: clusterv1.Bootstrap{
				DataSecretName: new("machine-1-bootstrap-data"),
				ConfigRef:      clusterv1.ContractVersionedObjectReference{APIGroup: bootstrapv1beta1.GroupVersion.Group, Kind: "TalosConfig", Name: "machine-1"},
			},
		},
		// Deliberately no status: cleanupMachine drops it before the request is sent.
	}

	return &updateFixture{
		handler: handler,
		node:    node,
		request: &runtimehooksv1.UpdateMachineRequest{
			Desired: runtimehooksv1.UpdateMachineRequestObjects{
				Machine:         machine,
				BootstrapConfig: talosConfig(desiredSpec),
			},
		},
	}
}

// markControlPlane labels the fixture's Machine as a control plane machine, which is what makes
// the handler wait for its kube-apiserver.
func (f *updateFixture) markControlPlane() {
	f.request.Desired.Machine.Labels = map[string]string{clusterv1.MachineControlPlaneLabel: ""}
}

// update runs one UpdateMachine call and returns the response.
func (f *updateFixture) update(t *testing.T) *runtimehooksv1.UpdateMachineResponse {
	t.Helper()

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)
	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)

	return resp
}

// freshHash is the hash CABPT would stamp on a secret regenerated for the fixture's desired
// spec and Kubernetes version.
func freshHash(t *testing.T) string {
	t.Helper()

	hash, err := bootstrapv1beta1.InPlaceConfigHash(
		bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"},
		"1.34.0",
		"",
	)
	require.NoError(t, err)

	return hash
}

// The most dangerous failure mode: acting on a data secret that still holds the pre-update
// configuration would apply a no-op and then report the in-place update complete, silently
// dropping the user's change.
func TestUpdateMachine_WaitsForRegeneratedConfig(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", "stale-hash", "")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds, "should report still-in-progress while the secret is stale")
	assert.Empty(t, f.node.applied, "must not apply a configuration that predates the update")
	assert.Empty(t, f.node.upgrades)
}

func TestUpdateMachine_AppliesConfigWhenFresh(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1)
	assert.Empty(t, f.node.upgrades, "running version already matches the desired image")
	assert.Zero(t, resp.RetryAfterSeconds, "config-only change should complete")
	assert.Zero(t, f.node.apiServerCalls, "a worker has no kube-apiserver to wait for")
}

// withPlaceholderKubelet adds the kubelet provider-id argument, set to the placeholder, to a
// machine configuration from machineConfigWithImage.
func withPlaceholderKubelet(config string) string {
	return strings.Replace(config, "machine:\n", "machine:\n  kubelet:\n    extraArgs:\n      provider-id: PROVIDER_ID\n", 1)
}

// setSecretData replaces the fixture's bootstrap data, keeping its hash annotation.
func (f *updateFixture) setSecretData(t *testing.T, data string) {
	t.Helper()

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: "default", Name: "machine-1-bootstrap-data"}
	require.NoError(t, f.handler.client.Get(context.Background(), key, secret))
	secret.Data["value"] = []byte(data)
	require.NoError(t, f.handler.client.Update(context.Background(), secret))
}

// The bootstrap data carries the provider ID placeholder that the infrastructure provider
// replaces when it delivers the configuration at provisioning. The in-place path delivers it
// itself, so it must replace it too: applying the literal would hand the running kubelet
// "PROVIDER_ID" as its provider ID.
func TestUpdateMachine_ReplacesTheProviderIDPlaceholder(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")
	f.setSecretData(t, withPlaceholderKubelet(machineConfigWithImage("ghcr.io/siderolabs/installer:v1.13.0")))
	f.request.Desired.Machine.Spec.ProviderID = "tinkerbell://default/hw-1"

	resp := f.update(t)

	assert.Zero(t, resp.RetryAfterSeconds)
	require.Len(t, f.node.applied, 1)
	assert.Contains(t, string(f.node.applied[0]), "provider-id: tinkerbell://default/hw-1")
	assert.NotContains(t, string(f.node.applied[0]), providerIDPlaceholder)
}

// Without a provider ID there is nothing to put in the placeholder's place, and applying the
// literal is exactly what must not happen.
func TestUpdateMachine_RefusesThePlaceholderWithoutAProviderID(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")
	f.setSecretData(t, withPlaceholderKubelet(machineConfigWithImage("ghcr.io/siderolabs/installer:v1.13.0")))

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	assert.Equal(t, runtimehooksv1.ResponseStatusFailure, resp.Status)
	assert.Contains(t, resp.Message, "failed to resolve desired machine configuration")
	assert.Empty(t, f.node.applied, "must not apply the placeholder")
}

// A configuration without the placeholder is applied as it is, provider ID or not.
func TestWithProviderIDLeavesDataWithoutThePlaceholderAlone(t *testing.T) {
	t.Parallel()

	data := []byte(machineConfigWithImage("ghcr.io/siderolabs/installer:v1.13.0"))

	got, err := withProviderID(data, &clusterv1.Machine{})
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// Talos restarts kube-apiserver itself when the configuration changes what it runs with. The
// update is only complete once the kubelet has replaced the pod and the new one is ready;
// reporting it earlier would let the owner move on with this node's API server still down.
func TestUpdateMachine_WaitsForTheAPIServerOnAControlPlaneMachine(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")
	f.markControlPlane()

	start := time.Now()
	f.handler.applies.now = func() time.Time { return start }
	f.node.setAPIServer(APIServerState{rendered: "3", started: start.Add(-time.Hour), ready: true})

	// Applied; Talos has not reacted yet.
	resp := f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Contains(t, resp.Message, "settle")
	assert.Len(t, f.node.applied, 1)

	// Talos re-rendered the static pod, the kubelet has not replaced the pod yet.
	f.node.setAPIServer(APIServerState{rendered: "4", started: start.Add(-time.Hour), ready: true})
	resp = f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Contains(t, resp.Message, "restart kube-apiserver")

	// Replaced, still starting.
	f.node.setAPIServer(APIServerState{rendered: "4", started: start.Add(5 * time.Second), ready: false})
	resp = f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Contains(t, resp.Message, "become ready")

	// Ready on the new configuration.
	f.node.setAPIServer(APIServerState{rendered: "4", started: start.Add(5 * time.Second), ready: true})
	resp = f.update(t)
	assert.Zero(t, resp.RetryAfterSeconds)

	_, tracked := f.handler.applies.get(types.NamespacedName{Namespace: "default", Name: "machine-1"}, configHash(f.node.applied[0]))
	assert.False(t, tracked, "a settled apply is forgotten")
}

// A change that does not touch kube-apiserver leaves the static pod alone: once Talos had time to
// react, a ready API server is all that is required.
func TestUpdateMachine_SettlesWhenTheChangeLeavesTheAPIServerAlone(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")
	f.markControlPlane()

	clock := time.Now()
	f.handler.applies.now = func() time.Time { return clock }
	f.node.setAPIServer(APIServerState{rendered: "3", started: clock.Add(-time.Hour), ready: false})

	resp := f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Contains(t, resp.Message, "settle")

	clock = clock.Add(apiServerSettleWindow)

	resp = f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds, "the API server is not ready")
	assert.Contains(t, resp.Message, "become ready")

	f.node.setAPIServer(APIServerState{rendered: "3", started: clock.Add(-time.Hour), ready: true})

	resp = f.update(t)
	assert.Zero(t, resp.RetryAfterSeconds)
	assert.Len(t, f.node.applied, 3, "the configuration is re-applied on every call, a no-op on the node")
}

// Cluster API stops calling the hook once the Machine is deleting, so the extension has to notice
// the deletion itself and stop a Talos upgrade that would otherwise reboot the node on its way out.
func TestUpdateMachine_CancelsTheUpgradeOfAMachineBeingDeleted(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "factory.talos.dev/metal-installer/abc:v1.14.0", freshHash(t), "")
	f.node.blockUpgrades = true
	f.handler.deletionPollInterval = 10 * time.Millisecond

	resp := f.update(t)
	assert.Positive(t, resp.RetryAfterSeconds)

	require.Eventually(t, func() bool { return len(f.node.upgradesSeen()) == 1 }, time.Second, 10*time.Millisecond)

	// A Machine carries finalizers, so its deletion only stamps a timestamp at first.
	key := types.NamespacedName{Namespace: "default", Name: "machine-1"}
	live := &clusterv1.Machine{}
	require.NoError(t, f.handler.client.Get(context.Background(), key, live))
	live.Finalizers = []string{"test.example/hold"}
	require.NoError(t, f.handler.client.Update(context.Background(), live))
	require.NoError(t, f.handler.client.Delete(context.Background(), live))

	require.Eventually(t, func() bool {
		_, tracked := f.handler.upgrades.get(key)

		return !tracked
	}, 2*time.Second, 10*time.Millisecond, "the upgrade must be cancelled and forgotten")

	require.Eventually(t, func() bool { return f.node.upgradeContextError() != nil }, time.Second, 10*time.Millisecond)
	assert.ErrorIs(t, f.node.upgradeContextError(), context.Canceled)
}

func TestUpdateMachine_UsesTheMachineServiceUpgradeOnNodesBefore113(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.12.3", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t), "")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1, "config is applied before the upgrade so the new image is recorded")
	assert.Equal(t, []string{"ghcr.io/siderolabs/installer:v1.13.0"}, f.node.legacyUpgrades)
	assert.Empty(t, f.node.upgrades, "a pre-1.13 node has no lifecycle service")
	assert.Positive(t, resp.RetryAfterSeconds, "must keep polling while the node reboots")
}

func TestUpdateMachine_StartsOneLifecycleUpgradeAndKeepsPolling(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "factory.talos.dev/metal-installer/abc:v1.14.0", freshHash(t), "")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Contains(t, resp.Message, "factory.talos.dev/metal-installer/abc:v1.14.0")

	require.Eventually(t, func() bool { return len(f.node.upgradesSeen()) == 1 }, time.Second, 10*time.Millisecond, "the sequence runs in the background")

	// The node still reports the old version: the sequence is finished, the reboot pending.
	resp = &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds)
	assert.Len(t, f.node.upgradesSeen(), 1, "a finished sequence is not started again while the node reboots")
	assert.Empty(t, f.node.legacyUpgrades)

	// Back on the new version: the update completes and the sequence is forgotten.
	f.node.setVersion("v1.14.0")
	resp = &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Zero(t, resp.RetryAfterSeconds)
	_, tracked := f.handler.upgrades.get(types.NamespacedName{Namespace: "default", Name: "machine-1"})
	assert.False(t, tracked)
}

func TestUpdateMachine_ReportsAFailedLifecycleUpgradeOnceThenRetries(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "factory.talos.dev/metal-installer/abc:v1.14.0", freshHash(t), "")
	f.node.upgradeErr = errors.New("the upgrade exited with code 1: bad image")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)
	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)

	require.Eventually(t, func() bool {
		current, ok := f.handler.upgrades.get(types.NamespacedName{Namespace: "default", Name: "machine-1"})

		return ok && current.done
	}, time.Second, 10*time.Millisecond)

	resp = &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)
	assert.Equal(t, runtimehooksv1.ResponseStatusFailure, resp.Status)
	assert.Contains(t, resp.Message, "failed to upgrade Talos")

	// The failure was surfaced once; the next call starts the sequence over.
	resp = &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)
	assert.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	require.Eventually(t, func() bool { return len(f.node.upgradesSeen()) == 2 }, time.Second, 10*time.Millisecond)
}

func TestImageTag(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		image string
		want  string
	}{
		{"ghcr.io/siderolabs/installer:v1.13.0", "v1.13.0"},
		{"registry.local:5000/installer:v1.13.0", "v1.13.0"},
		{"registry.local:5000/installer", ""},
		{"ghcr.io/siderolabs/installer@sha256:abc", ""},
		{"installer", ""},
	} {
		assert.Equal(t, tt.want, imageTag(tt.image), tt.image)
	}
}

// An image whose tag cannot be read must not trigger an upgrade: guessing wrong there would
// reboot the node on every single reconcile.
func TestNeedsUpgrade(t *testing.T) {
	t.Parallel()

	assert.True(t, needsUpgrade("ghcr.io/siderolabs/installer:v1.13.0", "v1.12.3"))
	assert.False(t, needsUpgrade("ghcr.io/siderolabs/installer:v1.13.0", "v1.13.0"))
	assert.False(t, needsUpgrade("ghcr.io/siderolabs/installer@sha256:abc", "v1.13.0"))
	assert.False(t, needsUpgrade("", "v1.13.0"))
	assert.False(t, needsUpgrade("ghcr.io/siderolabs/installer:v1.13.0", ""))
}

// A floating tag has no version to compare against the running one, so treating it as an upgrade
// would reboot the node, come back on the same tag, and reboot again forever.
func TestNeedsUpgradeIgnoresUnversionedTags(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		image, running string
		want           bool
	}{
		"newer version":      {"ghcr.io/siderolabs/installer:v1.14.0", "v1.13.9", true},
		"same version":       {"ghcr.io/siderolabs/installer:v1.13.9", "v1.13.9", false},
		"latest":             {"ghcr.io/siderolabs/installer:latest", "v1.13.9", false},
		"edge":               {"ghcr.io/siderolabs/installer:edge", "v1.13.9", false},
		"no tag":             {"ghcr.io/siderolabs/installer", "v1.13.9", false},
		"digest only":        {"ghcr.io/siderolabs/installer@sha256:abc", "v1.13.9", false},
		"registry port only": {"registry.local:5000/installer", "v1.13.9", false},
		"prerelease":         {"ghcr.io/siderolabs/installer:v1.14.0-alpha.1", "v1.13.9", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, needsUpgrade(tc.image, tc.running))
		})
	}
}

// The resolved installer image lives in TalosConfig status, which Cluster API strips from the
// hook request; the handler must read the live object or every regenerated secret looks stale.
func TestUpdateMachine_HashesTheRecordedInstallerImage(t *testing.T) {
	t.Parallel()

	const image = "factory.example.test/metal-installer/abc:v1.13.0"

	hash, err := bootstrapv1beta1.InPlaceConfigHash(
		bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"}, "1.34.0", image)
	require.NoError(t, err)

	f := newUpdateFixture(t, "v1.13.0", image, hash, image)

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1, "a secret hashed with the recorded image is fresh")
	assert.Zero(t, resp.RetryAfterSeconds)
}

func TestUpdateMachine_WaitsWhenTheSecretPredatesTheImage(t *testing.T) {
	t.Parallel()

	const image = "factory.example.test/metal-installer/abc:v1.13.0"

	// Hashed without the image: what a secret rendered before spec.imageFactory was set carries.
	f := newUpdateFixture(t, "v1.13.0", image, freshHash(t), image)

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds, "must wait for CABPT to re-render with the image")
	assert.Empty(t, f.node.applied)
}

// machineConfigWithDocument renders a configuration whose installer image lives only in an
// UnattendedInstallConfig document, the 1.14 shape.
func machineConfigWithDocument(image string) string {
	return fmt.Sprintf(`version: v1alpha1
debug: false
machine:
  type: worker
  token: aaaaaa.bbbbbbbbbbbbbbbb
  ca:
    crt: ""
    key: ""
cluster:
  id: dGVzdA==
  secret: dGVzdA==
  controlPlane:
    endpoint: https://192.168.1.1:6443
  token: aaaaaa.bbbbbbbbbbbbbbbb
  ca:
    crt: ""
    key: ""
---
apiVersion: v1alpha1
kind: UnattendedInstallConfig
installer:
  image: %s
provisioning:
  diskSelector:
    match: system_disk
`, image)
}

func TestInstallImageReadsTheUnattendedInstallDocument(t *testing.T) {
	t.Parallel()

	image, err := installImage([]byte(machineConfigWithDocument("factory.talos.dev/metal-installer/abc:v1.14.0")))
	require.NoError(t, err)
	assert.Equal(t, "factory.talos.dev/metal-installer/abc:v1.14.0", image)
}

func TestInstallImageReadsTheLegacyField(t *testing.T) {
	t.Parallel()

	image, err := installImage([]byte(machineConfigWithImage("ghcr.io/siderolabs/installer:v1.13.0")))
	require.NoError(t, err)
	assert.Equal(t, "ghcr.io/siderolabs/installer:v1.13.0", image)
}
