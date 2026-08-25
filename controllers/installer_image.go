// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// InstallerImageStatusField is the InfrastructureMachine status field an infrastructure
// provider may publish to declare which Talos installer image a machine should run.
//
// The Cluster API Provider Tinkerbell resolves this from an Image Factory schematic built out
// of the machine's actual hardware, so a node with NVMe storage gets the NVMe tooling without
// anyone hand-maintaining an image reference. Any provider that publishes the same field gets
// the same behaviour; this package deliberately knows nothing about Tinkerbell.
const InstallerImageStatusField = "installerImage"

// installerImageFor reads the installer image the infrastructure provider resolved for a
// machine, or an empty string when there is none.
//
// An absent image is normal rather than exceptional: providers that do not resolve schematics
// never set it, machines are reconciled before their infrastructure is created, and the value
// only becomes knowable once hardware has been selected. In every one of those cases the
// correct behaviour is to generate a configuration without an install image override and pick
// the value up on a later reconcile.
func installerImageFor(ctx context.Context, c client.Client, owner *unstructured.Unstructured) (string, error) {
	if owner == nil {
		return "", nil
	}

	machine := &capiv1.Machine{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(owner.Object, machine); err != nil {
		return "", fmt.Errorf("decoding config owner as a Machine: %w", err)
	}

	ref := machine.Spec.InfrastructureRef
	if !ref.IsDefined() {
		return "", nil
	}

	infra := &unstructured.Unstructured{}
	infra.SetGroupVersionKind(schema.GroupVersionKind{
		Group: ref.APIGroup,
		// The contract-versioned reference carries no version. Any served version exposes the
		// same status field, so the stored version resolves it.
		Version: "v1beta2",
		Kind:    ref.Kind,
	})

	key := types.NamespacedName{Namespace: machine.Namespace, Name: ref.Name}
	if err := c.Get(ctx, key, infra); err != nil {
		// Not being able to read the InfraMachine must not block bootstrap generation, which is
		// itself a prerequisite for that object being provisioned.
		return "", nil //nolint:nilerr // absence is the expected case, see doc comment
	}

	image, found, err := unstructured.NestedString(infra.Object, "status", InstallerImageStatusField)
	if err != nil || !found {
		return "", nil
	}

	return image, nil
}

// installImagePatch renders a strategic merge patch pinning the Talos installer image.
//
// It is applied ahead of any user-supplied strategic patches so that an explicit patch in the
// TalosConfig still wins. The resolved image is a good default, not an override of intent.
func installImagePatch(image string) string {
	return fmt.Sprintf("machine:\n  install:\n    image: %s\n", image)
}
