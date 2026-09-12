// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

// talosConfig builds a TalosConfig as it arrives on the wire.
func talosConfig(spec map[string]any) runtime.RawExtension {
	return rawObject(map[string]any{
		"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta1",
		"kind":       "TalosConfig",
		"spec":       spec,
	})
}

// tinkerbellMachine builds a TinkerbellMachine as it arrives on the wire.
func tinkerbellMachine(spec map[string]any) runtime.RawExtension {
	return rawObject(map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "TinkerbellMachine",
		"spec":       spec,
	})
}

func rawObject(obj map[string]any) runtime.RawExtension {
	encoded, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return runtime.RawExtension{Raw: encoded}
}

// applyPatch mirrors what Cluster API does with the extension's answer: it applies the
// returned patch to the current object. The update may proceed in-place only if the result
// equals the desired object.
func applyPatch(t *testing.T, current runtime.RawExtension, patch runtimehooksv1.Patch) []byte {
	t.Helper()

	if !patch.IsDefined() {
		return current.Raw
	}

	require.Equal(t, runtimehooksv1.JSONMergePatchType, patch.PatchType)

	patched, err := jsonpatch.MergePatch(current.Raw, patch.Patch)
	require.NoError(t, err)

	return patched
}

// assertCovers asserts that applying the patch to current fully reaches desired, i.e. that
// the extension claimed the whole diff and Cluster API will update in place.
func assertCovers(t *testing.T, current, desired runtime.RawExtension, patch runtimehooksv1.Patch) {
	t.Helper()

	patched := applyPatch(t, current, patch)

	var got, want any

	require.NoError(t, json.Unmarshal(patched, &got))
	require.NoError(t, json.Unmarshal(desired.Raw, &want))

	assert.Equal(t, want, got, "patch did not fully cover the diff, Cluster API would fall back to a rollout")
}

// assertDeclines asserts that the patch leaves a difference behind, so Cluster API falls
// back to a rolling replacement.
func assertDeclines(t *testing.T, current, desired runtime.RawExtension, patch runtimehooksv1.Patch) {
	t.Helper()

	patched := applyPatch(t, current, patch)

	var got, want any

	require.NoError(t, json.Unmarshal(patched, &got))
	require.NoError(t, json.Unmarshal(desired.Raw, &want))

	assert.NotEqual(t, want, got, "extension claimed a change it should have declined")
}

func TestCanUpdateMachine_BootstrapConfig(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		current  map[string]any
		desired  map[string]any
		declines bool
	}{
		{
			name:    "config patches change is absorbed",
			current: map[string]any{"generateType": "worker", "configPatches": []any{map[string]any{"op": "add"}}},
			desired: map[string]any{"generateType": "worker", "configPatches": []any{map[string]any{"op": "replace"}}},
		},
		{
			name:    "talos version change is absorbed",
			current: map[string]any{"generateType": "worker", "talosVersion": "v1.12"},
			desired: map[string]any{"generateType": "worker", "talosVersion": "v1.13"},
		},
		{
			name:    "strategic patches change is absorbed",
			current: map[string]any{"generateType": "worker", "strategicPatches": []any{"a"}},
			desired: map[string]any{"generateType": "worker", "strategicPatches": []any{"b"}},
		},
		{
			name:    "adding an image factory block is absorbed",
			current: map[string]any{"generateType": "worker"},
			desired: map[string]any{"generateType": "worker", "imageFactory": map[string]any{"extensions": []any{"siderolabs/nvme-cli"}}},
		},
		{
			name: "image factory change is absorbed",
			current: map[string]any{"generateType": "worker", "imageFactory": map[string]any{
				"extensions": []any{"siderolabs/nvme-cli"},
			}},
			desired: map[string]any{"generateType": "worker", "imageFactory": map[string]any{
				"extensions": []any{"siderolabs/intel-ucode", "siderolabs/nvme-cli"},
				"bootloader": "sd-boot",
			}},
		},
		{
			name:    "clearing a field is absorbed",
			current: map[string]any{"generateType": "worker", "talosVersion": "v1.12"},
			desired: map[string]any{"generateType": "worker"},
		},
		{
			name:    "no change yields no patch",
			current: map[string]any{"generateType": "worker", "talosVersion": "v1.13"},
			desired: map[string]any{"generateType": "worker", "talosVersion": "v1.13"},
		},
		{
			name:     "role change is declined",
			current:  map[string]any{"generateType": "worker"},
			desired:  map[string]any{"generateType": "controlplane"},
			declines: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			current := talosConfig(tt.current)
			desired := talosConfig(tt.desired)

			h := &Handler{}
			resp := &runtimehooksv1.CanUpdateMachineResponse{}

			h.DoCanUpdateMachine(context.Background(), &runtimehooksv1.CanUpdateMachineRequest{
				Current: runtimehooksv1.CanUpdateMachineRequestObjects{
					Machine:               clusterv1.Machine{},
					InfrastructureMachine: tinkerbellMachine(map[string]any{}),
					BootstrapConfig:       current,
				},
				Desired: runtimehooksv1.CanUpdateMachineRequestObjects{
					Machine:               clusterv1.Machine{},
					InfrastructureMachine: tinkerbellMachine(map[string]any{}),
					BootstrapConfig:       desired,
				},
			}, resp)

			require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)

			if tt.declines {
				assertDeclines(t, current, desired, resp.BootstrapConfigPatch)
			} else {
				assertCovers(t, current, desired, resp.BootstrapConfigPatch)
			}
		})
	}
}

func TestCanUpdateMachine_InfrastructureMachine(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		current  map[string]any
		desired  map[string]any
		declines bool
	}{
		{
			name:    "tink template change is absorbed",
			current: map[string]any{"templateInline": "old", "hardwareName": "hw-1"},
			desired: map[string]any{"templateInline": "new", "hardwareName": "hw-1"},
		},
		{
			name:    "hardware affinity change is absorbed",
			current: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"a"}}},
			desired: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"b"}}},
		},
		{
			name:    "iso url change is absorbed",
			current: map[string]any{"bootOptions": map[string]any{"isoURL": "http://a/x.iso", "bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{"isoURL": "http://b/x.iso", "bootMode": "isoboot"}},
		},
		{
			name:     "boot mode change is declined",
			current:  map[string]any{"bootOptions": map[string]any{"bootMode": "isoboot"}},
			desired:  map[string]any{"bootOptions": map[string]any{"bootMode": "netboot"}},
			declines: true,
		},
		{
			name:     "hardware name change is declined",
			current:  map[string]any{"hardwareName": "hw-1"},
			desired:  map[string]any{"hardwareName": "hw-2"},
			declines: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			current := tinkerbellMachine(tt.current)
			desired := tinkerbellMachine(tt.desired)

			h := &Handler{}
			resp := &runtimehooksv1.CanUpdateMachineResponse{}

			h.DoCanUpdateMachine(context.Background(), &runtimehooksv1.CanUpdateMachineRequest{
				Current: runtimehooksv1.CanUpdateMachineRequestObjects{InfrastructureMachine: current},
				Desired: runtimehooksv1.CanUpdateMachineRequestObjects{InfrastructureMachine: desired},
			}, resp)

			require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)

			if tt.declines {
				assertDeclines(t, current, desired, resp.InfrastructureMachinePatch)
			} else {
				assertCovers(t, current, desired, resp.InfrastructureMachinePatch)
			}
		})
	}
}

// An infrastructure provider with no known policy must never have its changes claimed:
// whether a field is inert is a property of that provider's controller and cannot be
// guessed, so the safe answer is to fall back to a rolling replacement.
func TestCanUpdateMachine_UnknownInfraProviderIsDeclined(t *testing.T) {
	t.Parallel()

	current := rawObject(map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "DockerMachine",
		"spec":       map[string]any{"customImage": "old"},
	})
	desired := rawObject(map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "DockerMachine",
		"spec":       map[string]any{"customImage": "new"},
	})

	h := &Handler{}
	resp := &runtimehooksv1.CanUpdateMachineResponse{}

	h.DoCanUpdateMachine(context.Background(), &runtimehooksv1.CanUpdateMachineRequest{
		Current: runtimehooksv1.CanUpdateMachineRequestObjects{InfrastructureMachine: current},
		Desired: runtimehooksv1.CanUpdateMachineRequestObjects{InfrastructureMachine: desired},
	}, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status)
	assert.False(t, resp.InfrastructureMachinePatch.IsDefined())
	assertDeclines(t, current, desired, resp.InfrastructureMachinePatch)
}

func TestCanUpdateMachine_KubernetesVersion(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	resp := &runtimehooksv1.CanUpdateMachineResponse{}

	h.DoCanUpdateMachine(context.Background(), &runtimehooksv1.CanUpdateMachineRequest{
		Current: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine: clusterv1.Machine{Spec: clusterv1.MachineSpec{Version: "v1.33.0"}},
		},
		Desired: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine: clusterv1.Machine{Spec: clusterv1.MachineSpec{Version: "v1.34.0"}},
		},
	}, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status)
	require.True(t, resp.MachinePatch.IsDefined())

	var patch map[string]any
	require.NoError(t, json.Unmarshal(resp.MachinePatch.Patch, &patch))

	assert.Equal(t, map[string]any{"spec": map[string]any{"version": "v1.34.0"}}, patch)
}

func TestCanUpdateMachineSet_UsesTemplateSpecPaths(t *testing.T) {
	t.Parallel()

	current := rawObject(map[string]any{
		"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta1",
		"kind":       "TalosConfigTemplate",
		"spec": map[string]any{
			"template": map[string]any{"spec": map[string]any{"generateType": "worker", "talosVersion": "v1.12"}},
		},
	})
	desired := rawObject(map[string]any{
		"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta1",
		"kind":       "TalosConfigTemplate",
		"spec": map[string]any{
			"template": map[string]any{"spec": map[string]any{"generateType": "worker", "talosVersion": "v1.13"}},
		},
	})

	h := &Handler{}
	resp := &runtimehooksv1.CanUpdateMachineSetResponse{}

	h.DoCanUpdateMachineSet(context.Background(), &runtimehooksv1.CanUpdateMachineSetRequest{
		Current: runtimehooksv1.CanUpdateMachineSetRequestObjects{BootstrapConfigTemplate: current},
		Desired: runtimehooksv1.CanUpdateMachineSetRequestObjects{BootstrapConfigTemplate: desired},
	}, resp)

	require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)
	assertCovers(t, current, desired, resp.BootstrapConfigTemplatePatch)
}
