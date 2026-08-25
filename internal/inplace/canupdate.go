// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

// templateSpecPrefix is where a *Template object keeps the spec of the objects it stamps out.
const templateSpecPrefix = "spec.template.spec"

// DoCanUpdateMachine answers whether the changes between the current and desired Machine,
// InfraMachine and BootstrapConfig can be delivered in place.
//
// The answer is expressed as per-object patches rather than a boolean: Cluster API applies
// them to the current objects and compares against desired, falling back to a rolling
// replacement if anything is left over. See mergePatchForPaths.
func (h *Handler) DoCanUpdateMachine(ctx context.Context, req *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse) {
	log := ctrl.LoggerFrom(ctx)

	machinePatch, err := patchBetween(req.Current.Machine, req.Desired.Machine, machinePaths)
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute Machine patch", err, log)

		return
	}

	bootstrapPatch, err := patchBetweenRaw(req.Current.BootstrapConfig, req.Desired.BootstrapConfig, talosConfigPaths)
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute BootstrapConfig patch", err, log)

		return
	}

	infraPatch, err := h.infraPatch(req.Current.InfrastructureMachine, req.Desired.InfrastructureMachine, "spec")
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute InfrastructureMachine patch", err, log)

		return
	}

	resp.MachinePatch = machinePatch
	resp.BootstrapConfigPatch = bootstrapPatch
	resp.InfrastructureMachinePatch = infraPatch
	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// DoCanUpdateMachineSet answers the same question for a MachineSet and its templates.
//
// The shape is identical to DoCanUpdateMachine except that the interesting fields sit under
// spec.template.spec rather than spec.
func (h *Handler) DoCanUpdateMachineSet(ctx context.Context, req *runtimehooksv1.CanUpdateMachineSetRequest, resp *runtimehooksv1.CanUpdateMachineSetResponse) {
	log := ctrl.LoggerFrom(ctx)

	machineSetPatch, err := patchBetween(req.Current.MachineSet, req.Desired.MachineSet, prefixPaths(templateSpecPrefix, machinePaths))
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute MachineSet patch", err, log)

		return
	}

	bootstrapPatch, err := patchBetweenRaw(
		req.Current.BootstrapConfigTemplate,
		req.Desired.BootstrapConfigTemplate,
		prefixPaths(templateSpecPrefix, talosConfigPaths),
	)
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute BootstrapConfigTemplate patch", err, log)

		return
	}

	infraPatch, err := h.infraPatch(
		req.Current.InfrastructureMachineTemplate,
		req.Desired.InfrastructureMachineTemplate,
		templateSpecPrefix,
	)
	if err != nil {
		respondFailure(&resp.CommonResponse, "failed to compute InfrastructureMachineTemplate patch", err, log)

		return
	}

	resp.MachineSetPatch = machineSetPatch
	resp.BootstrapConfigTemplatePatch = bootstrapPatch
	resp.InfrastructureMachineTemplatePatch = infraPatch
	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// infraPatch computes the InfraMachine patch for whichever infrastructure provider is in
// use, consulting the provisioning-time-only allowlist for its kind.
//
// An unknown provider yields no patch, which makes any infrastructure change fall back to a
// rolling replacement.
func (h *Handler) infraPatch(current, desired runtime.RawExtension, prefix string) (runtimehooksv1.Patch, error) {
	currentObj, err := rawToUnstructured(current)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	desiredObj, err := rawToUnstructured(desired)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	if currentObj == nil || desiredObj == nil {
		return runtimehooksv1.Patch{}, nil
	}

	gk, err := groupKindOf(desiredObj)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	paths, ok := infraPathsFor(gk)
	if !ok {
		return runtimehooksv1.Patch{}, nil
	}

	if prefix != "spec" {
		paths = prefixPaths(prefix, paths)
	}

	return mergePatchForPaths(currentObj, desiredObj, paths)
}

// patchBetween computes a patch between two typed objects.
func patchBetween(current, desired any, paths []fieldPath) (runtimehooksv1.Patch, error) {
	currentObj, err := toUnstructured(current)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	desiredObj, err := toUnstructured(desired)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	return mergePatchForPaths(currentObj, desiredObj, paths)
}

// patchBetweenRaw computes a patch between two objects delivered as raw JSON.
//
// The BootstrapConfig is optional in the hook contract, so an absent object on either side
// yields no patch rather than an error.
func patchBetweenRaw(current, desired runtime.RawExtension, paths []fieldPath) (runtimehooksv1.Patch, error) {
	currentObj, err := rawToUnstructured(current)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	desiredObj, err := rawToUnstructured(desired)
	if err != nil {
		return runtimehooksv1.Patch{}, err
	}

	if currentObj == nil || desiredObj == nil {
		return runtimehooksv1.Patch{}, nil
	}

	return mergePatchForPaths(currentObj, desiredObj, paths)
}

// rawToUnstructured decodes a RawExtension, returning nil when it carries no object.
func rawToUnstructured(raw runtime.RawExtension) (map[string]any, error) {
	if len(raw.Raw) > 0 {
		out := map[string]any{}
		if err := json.Unmarshal(raw.Raw, &out); err != nil {
			return nil, fmt.Errorf("failed to decode raw object: %w", err)
		}

		return out, nil
	}

	if raw.Object != nil {
		return toUnstructured(raw.Object)
	}

	return nil, nil
}

// groupKindOf reads the GroupKind out of a decoded object's apiVersion and kind.
func groupKindOf(obj map[string]any) (schema.GroupKind, error) {
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)

	if apiVersion == "" || kind == "" {
		return schema.GroupKind{}, fmt.Errorf("object is missing apiVersion or kind")
	}

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupKind{}, fmt.Errorf("failed to parse apiVersion %q: %w", apiVersion, err)
	}

	return schema.GroupKind{Group: gv.Group, Kind: kind}, nil
}
