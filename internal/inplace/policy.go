// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// talosConfigPaths are the TalosConfig spec fields an in-place update can deliver.
//
// All of them feed machine configuration generation, so a change is realised by
// regenerating the config and applying it to the running node. spec.imageFactory belongs
// here because it only changes machine.install.image: the config is applied without a
// reboot, and UpdateMachine upgrades the node only when the image tag names a Talos
// version the node is not already running.
//
// spec.generateType is deliberately absent: it selects the machine's role, and turning a
// worker into a control plane node is not something applying a config can accomplish. That
// change must remain a rolling replacement.
var talosConfigPaths = []fieldPath{
	"spec.configPatches",
	"spec.strategicPatches",
	"spec.data",
	"spec.hostname",
	"spec.talosVersion",
	"spec.imageFactory",
}

// machinePaths are the core Machine spec fields an in-place update can deliver.
//
// The Kubernetes version reaches the node through the machine configuration, which carries
// the control plane component and kubelet images, so a version bump is delivered by the
// same regenerate-and-apply path as any other config change.
var machinePaths = []fieldPath{
	"spec.version",
}

// InfraPolicy names the InfrastructureMachine spec fields that are provisioning-time only:
// they influence how a machine is first built and are inert once it is running.
//
// Changing such a field on a running machine needs no action on the node at all, so the
// extension can absorb it and spare the cluster a rollout it would otherwise perform for no
// practical gain.
type InfraPolicy struct {
	// Paths are the spec field paths that may be absorbed in-place.
	Paths []fieldPath
}

// infraPolicies maps an InfrastructureMachine GroupKind to what may be absorbed for it.
//
// Providers absent from this map get no InfraMachine patch at all, so any infrastructure
// change falls back to a rolling replacement. That is the safe default: whether a field is
// truly inert is a property of the provider's controller, and cannot be guessed.
var infraPolicies = map[schema.GroupKind]InfraPolicy{
	{Group: "infrastructure.cluster.x-k8s.io", Kind: "TinkerbellMachine"}: {
		Paths: []fieldPath{
			// What a machine is provisioned from: the deprecated Tink template, or the v1alpha2
			// Task list with its per-entry boot options. Cluster API Provider Tinkerbell
			// short-circuits reconciliation once the Hardware carries
			// HardwareProvisionedAnnotation=true, and only creates a Workflow when none exists,
			// so editing these never re-images a running machine. They take effect the next time
			// the machine is genuinely provisioned. The same holds for the references and vars
			// CAPT puts on a Task Workflow: it reads them only when it creates the Workflow.
			"spec.templateInline",
			"spec.templateRef",
			"spec.tasks",
			"spec.references",
			"spec.vars",

			// Hardware selection already happened; spec.hardwareName is immutable in CAPT,
			// so an affinity change cannot move a running machine to different hardware.
			"spec.hardwareAffinity",

			// CAPT reads the boot options only when it creates the Workflow, so the whole field
			// (boot mode, ISO URL, customboot) is inert on a running machine, which boots from
			// disk. Absorbing it also lets a MachineDeployment move its boot options onto the
			// tasks entries without a rollout.
			"spec.bootOptions",

			// Deliberately excluded:
			//   spec.hardwareName     - immutable in CAPT
			//   spec.providerID       - immutable in CAPT
			//   spec.addressFromPool  - immutable in CAPT once Hardware is selected
		},
	},
}

// infraPathsFor returns the absorbable spec paths for an InfrastructureMachine kind, and
// whether a policy is known for it at all.
func infraPathsFor(gk schema.GroupKind) ([]fieldPath, bool) {
	policy, ok := infraPolicies[gk]
	if !ok {
		return nil, false
	}

	return policy.Paths, true
}
