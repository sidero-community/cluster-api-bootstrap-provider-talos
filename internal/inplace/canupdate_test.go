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
			// A merge patch merges objects, so a key removed inside one survives unless the
			// patch names it with null.
			name: "removing an image factory key is absorbed",
			current: map[string]any{"generateType": "worker", "imageFactory": map[string]any{
				"extensions": []any{"siderolabs/nvme-cli"},
				"bootloader": "sd-boot",
			}},
			desired: map[string]any{"generateType": "worker", "imageFactory": map[string]any{
				"extensions": []any{"siderolabs/nvme-cli"},
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
			name:    "boot mode change is absorbed",
			current: map[string]any{"bootOptions": map[string]any{"bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{"bootMode": "netboot"}},
		},
		{
			name: "tasks change is absorbed",
			current: map[string]any{"tasks": []any{
				map[string]any{"name": "a", "taskRef": map[string]any{"name": "x"}},
			}},
			desired: map[string]any{"tasks": []any{
				map[string]any{"name": "a", "taskRef": map[string]any{"name": "y"}, "bootOptions": map[string]any{"toggleNetboot": true}},
			}},
		},
		{
			name: "references change is absorbed",
			current: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
			}},
			desired: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "b"},
			}},
		},
		{
			name:    "vars change is absorbed",
			current: map[string]any{"vars": map[string]any{"k": "a"}},
			desired: map[string]any{"vars": map[string]any{"k": "b"}},
		},
		{
			name:    "removing the iso url is absorbed",
			current: map[string]any{"bootOptions": map[string]any{"isoURL": "http://a/x.iso", "bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{"bootMode": "netboot"}},
		},
		{
			name:    "emptying the boot options is absorbed",
			current: map[string]any{"bootOptions": map[string]any{"isoURL": "http://a/x.iso", "bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{}},
		},
		{
			name:    "removing the template ref namespace is absorbed",
			current: map[string]any{"templateRef": map[string]any{"name": "t", "namespace": "tink"}},
			desired: map[string]any{"templateRef": map[string]any{"name": "t"}},
		},
		{
			name:    "removing the preferred hardware affinity is absorbed",
			current: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"a"}, "preferred": []any{"b"}}},
			desired: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"a"}}},
		},
		{
			name: "removing a reference is absorbed",
			current: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
				"other": map[string]any{"version": "v1", "resource": "configmaps", "name": "c", "namespace": "capi"},
			}},
			desired: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
			}},
		},
		{
			name:    "removing a reference namespace is absorbed",
			current: map[string]any{"references": map[string]any{"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a", "namespace": "capi"}}},
			desired: map[string]any{"references": map[string]any{"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"}}},
		},
		{
			name:    "removing a var is absorbed",
			current: map[string]any{"vars": map[string]any{"k": "a", "gone": "b"}},
			desired: map[string]any{"vars": map[string]any{"k": "a"}},
		},
		{
			name:     "address pool change is declined",
			current:  map[string]any{"addressFromPool": map[string]any{"name": "p1"}},
			desired:  map[string]any{"addressFromPool": map[string]any{"name": "p2"}},
			declines: true,
		},
		{
			name:     "hardware name change is declined",
			current:  map[string]any{"hardwareName": "hw-1"},
			desired:  map[string]any{"hardwareName": "hw-2"},
			declines: true,
		},
		{
			// Removals are expressed only for allowlisted paths: one inside a field the
			// extension declines is left behind, so the machine still rolls out.
			name:     "removing a key inside the address pool is declined",
			current:  map[string]any{"addressFromPool": map[string]any{"apiGroup": "ipam.cluster.x-k8s.io", "name": "p1"}},
			desired:  map[string]any{"addressFromPool": map[string]any{"name": "p1"}},
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

// tinkerbellMachineTemplate builds a TinkerbellMachineTemplate as it arrives on the wire.
func tinkerbellMachineTemplate(kind string, spec map[string]any) runtime.RawExtension {
	return rawObject(map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       kind,
		"spec":       map[string]any{"template": map[string]any{"spec": spec}},
	})
}

// A MachineSet carries an InfraMachineTemplate, whose kind is the machine kind with a
// Template suffix. The allowlist is keyed by the machine kind, so the template must be
// mapped to it, or every infrastructure change on a MachineSet falls back to a rollout.
func TestCanUpdateMachineSet_InfrastructureMachineTemplate(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		kind     string
		current  map[string]any
		desired  map[string]any
		declines bool
	}{
		{
			name:    "tink template change is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"templateInline": "old", "bootOptions": map[string]any{"bootMode": "isoboot"}},
			desired: map[string]any{"templateInline": "new", "bootOptions": map[string]any{"bootMode": "isoboot"}},
		},
		{
			name:    "hardware affinity change is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"a"}}},
			desired: map[string]any{"hardwareAffinity": map[string]any{"required": []any{"b"}}},
		},
		{
			name:    "boot mode change is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"bootOptions": map[string]any{"bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{"bootMode": "netboot"}},
		},
		{
			name:    "tink template change alongside boot mode change is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"templateInline": "old", "bootOptions": map[string]any{"bootMode": "isoboot"}},
			desired: map[string]any{"templateInline": "new", "bootOptions": map[string]any{"bootMode": "netboot"}},
		},
		{
			// Moving from a template to tasks, boot options onto the entries, is the migration
			// CAPT's docs describe; it must not roll the machines out.
			name:    "template to tasks migration is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"templateInline": "old", "bootOptions": map[string]any{"bootMode": "netboot"}},
			desired: map[string]any{"tasks": []any{
				map[string]any{"name": "a", "taskInline": map[string]any{"actions": []any{}}, "bootOptions": map[string]any{"toggleNetboot": true, "bootMode": "netboot"}},
			}},
		},
		{
			name: "references change is absorbed",
			kind: "TinkerbellMachineTemplate",
			current: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
			}},
			desired: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "b"},
			}},
		},
		{
			name:    "vars change is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"vars": map[string]any{"k": "a"}},
			desired: map[string]any{"vars": map[string]any{"k": "b"}},
		},
		{
			name:    "removing the iso url is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"bootOptions": map[string]any{"isoURL": "http://a/x.iso", "bootMode": "isoboot"}},
			desired: map[string]any{"bootOptions": map[string]any{"bootMode": "netboot"}},
		},
		{
			name: "removing a reference is absorbed",
			kind: "TinkerbellMachineTemplate",
			current: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
				"other": map[string]any{"version": "v1", "resource": "configmaps", "name": "c"},
			}},
			desired: map[string]any{"references": map[string]any{
				"talos": map[string]any{"version": "v1beta1", "resource": "talosconfigs", "name": "a"},
			}},
		},
		{
			name:    "removing a var is absorbed",
			kind:    "TinkerbellMachineTemplate",
			current: map[string]any{"vars": map[string]any{"k": "a", "gone": "b"}},
			desired: map[string]any{"vars": map[string]any{"k": "a"}},
		},
		{
			name:     "address pool change is declined",
			kind:     "TinkerbellMachineTemplate",
			current:  map[string]any{"addressFromPool": map[string]any{"name": "p1"}},
			desired:  map[string]any{"addressFromPool": map[string]any{"name": "p2"}},
			declines: true,
		},
		{
			name:     "unknown provider template is declined",
			kind:     "DockerMachineTemplate",
			current:  map[string]any{"customImage": "old"},
			desired:  map[string]any{"customImage": "new"},
			declines: true,
		},
		{
			// Only a template kind names a machine kind; a machine kind handed over where a
			// template belongs is not something the allowlist can vouch for.
			name:     "machine kind in place of a template is declined",
			kind:     "TinkerbellMachine",
			current:  map[string]any{"templateInline": "old"},
			desired:  map[string]any{"templateInline": "new"},
			declines: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			current := tinkerbellMachineTemplate(tt.kind, tt.current)
			desired := tinkerbellMachineTemplate(tt.kind, tt.desired)

			h := &Handler{}
			resp := &runtimehooksv1.CanUpdateMachineSetResponse{}

			h.DoCanUpdateMachineSet(context.Background(), &runtimehooksv1.CanUpdateMachineSetRequest{
				Current: runtimehooksv1.CanUpdateMachineSetRequestObjects{InfrastructureMachineTemplate: current},
				Desired: runtimehooksv1.CanUpdateMachineSetRequestObjects{InfrastructureMachineTemplate: desired},
			}, resp)

			require.Equal(t, runtimehooksv1.ResponseStatusSuccess, resp.Status, resp.Message)

			if tt.declines {
				assertDeclines(t, current, desired, resp.InfrastructureMachineTemplatePatch)
			} else {
				assertCovers(t, current, desired, resp.InfrastructureMachineTemplatePatch)
			}
		})
	}
}
