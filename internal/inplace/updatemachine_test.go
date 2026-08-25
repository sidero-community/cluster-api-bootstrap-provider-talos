// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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

// fakeNode records what the update state machine did to a node.
type fakeNode struct {
	version string

	applied  [][]byte
	upgrades []string

	versionErr error
}

func (f *fakeNode) Version(context.Context) (string, error) { return f.version, f.versionErr }

func (f *fakeNode) ApplyConfig(_ context.Context, data []byte) error {
	f.applied = append(f.applied, data)

	return nil
}

func (f *fakeNode) Upgrade(_ context.Context, image string) error {
	f.upgrades = append(f.upgrades, image)

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
func newUpdateFixture(t *testing.T, runningVersion, configImage, secretHash string) *updateFixture {
	t.Helper()

	const (
		namespace = "default"
		cluster   = "test-cluster"
	)

	desiredSpec := map[string]any{"generateType": "worker", "talosVersion": "v1.13"}

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        "machine-1-bootstrap-data",
			Annotations: map[string]string{bootstrapv1beta1.InPlaceConfigHashAnnotation: secretHash},
		},
		Data: map[string][]byte{"value": []byte(machineConfigWithImage(configImage))},
	}

	node := &fakeNode{version: runningVersion}

	handler := NewHandler(
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
		func(context.Context, types.NamespacedName, []string) (NodeClient, error) { return node, nil },
	)

	machine := clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "machine-1"},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster,
			Version:     "v1.34.0",
			Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To("machine-1-bootstrap-data")},
		},
		Status: clusterv1.MachineStatus{
			Addresses: []clusterv1.MachineAddress{{Type: clusterv1.MachineInternalIP, Address: "192.168.1.10"}},
		},
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

// freshHash is the hash CABPT would stamp on a secret regenerated for the fixture's desired
// spec and Kubernetes version.
func freshHash(t *testing.T) string {
	t.Helper()

	hash, err := bootstrapv1beta1.InPlaceConfigHash(
		bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"},
		"1.34.0",
	)
	require.NoError(t, err)

	return hash
}

// The most dangerous failure mode: acting on a data secret that still holds the pre-update
// configuration would apply a no-op and then report the in-place update complete, silently
// dropping the user's change.
func TestUpdateMachine_WaitsForRegeneratedConfig(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", "stale-hash")

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Positive(t, resp.RetryAfterSeconds, "should report still-in-progress while the secret is stale")
	assert.Empty(t, f.node.applied, "must not apply a configuration that predates the update")
	assert.Empty(t, f.node.upgrades)
}

func TestUpdateMachine_AppliesConfigWhenFresh(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t))

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1)
	assert.Empty(t, f.node.upgrades, "running version already matches the desired image")
	assert.Zero(t, resp.RetryAfterSeconds, "config-only change should complete")
}

func TestUpdateMachine_UpgradesWhenInstallerImageDiffers(t *testing.T) {
	t.Parallel()

	f := newUpdateFixture(t, "v1.12.3", "ghcr.io/siderolabs/installer:v1.13.0", freshHash(t))

	resp := &runtimehooksv1.UpdateMachineResponse{}
	f.handler.DoUpdateMachine(context.Background(), f.request, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assert.Len(t, f.node.applied, 1, "config is applied before the upgrade so the new image is recorded")
	require.Len(t, f.node.upgrades, 1)
	assert.Equal(t, "ghcr.io/siderolabs/installer:v1.13.0", f.node.upgrades[0])
	assert.Positive(t, resp.RetryAfterSeconds, "must keep polling while the node reboots")
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
