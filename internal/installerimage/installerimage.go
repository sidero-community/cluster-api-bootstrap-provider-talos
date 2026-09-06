// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package installerimage resolves the Talos installer image for a machine.
//
// The primary source is the talos.tinkerbell.org/installer-image annotation on
// the machine's claimed Hardware, published per-machine by the
// talos-image-resolver runtime extension (Strategy B of the runtime-extensions
// migration): per-machine correctness for heterogeneous pools, and no
// dependency on any infrastructure provider's status. The InfraMachine
// status.installerImage read remains as a transition fallback until the
// infrastructure provider's resolution is retired; both the config writer and
// the in-place hash reader consume this one function, so the two can never
// disagree on the source (a disagreement would hash-mismatch every bootstrap
// secret forever).
package installerimage

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// AnnotationKey is the resolver-owned Hardware annotation carrying the
	// installer reference, e.g. factory.talos.dev/metal-installer/<id>:<ver>.
	AnnotationKey = "talos.tinkerbell.org/installer-image"

	// ownerNameLabel / ownerNamespaceLabel are the claim labels the
	// infrastructure provider stamps on Hardware; they locate a machine's
	// claimed Hardware without importing any Tinkerbell module.
	ownerNameLabel      = "v1alpha1.tinkerbell.org/ownerName"
	ownerNamespaceLabel = "v1alpha1.tinkerbell.org/ownerNamespace"
)

var hardwareListGVK = schema.GroupVersionKind{Group: "tinkerbell.org", Version: "v1alpha1", Kind: "HardwareList"}

// Resolve returns the installer image for the machine, or "" when none is
// knowable yet. Absence is normal, never an error: machines are reconciled
// before hardware is claimed, the resolver writes asynchronously, and
// providers without resolution never publish anything — in all cases the
// configuration generates without an install-image override and the value is
// picked up on a later reconcile.
func Resolve(ctx context.Context, c client.Reader, machine *capiv1.Machine) (string, error) {
	if machine == nil {
		return "", nil
	}
	ref := machine.Spec.InfrastructureRef
	if !ref.IsDefined() {
		return "", nil
	}

	if image := fromHardwareAnnotation(ctx, c, ref.Name, machine.Namespace); image != "" {
		return image, nil
	}
	return fromInfraMachineStatus(ctx, c, machine.Namespace, ref)
}

// fromHardwareAnnotation reads the resolver's annotation off the Hardware
// claimed by the named InfraMachine. Hardware lives in its own namespace, so
// the lookup is a label-selected list, not a name Get.
func fromHardwareAnnotation(ctx context.Context, c client.Reader, infraName, ownerNamespace string) string {
	hardware := &unstructured.UnstructuredList{}
	hardware.SetGroupVersionKind(hardwareListGVK)
	if err := c.List(ctx, hardware, client.MatchingLabels{
		ownerNameLabel:      infraName,
		ownerNamespaceLabel: ownerNamespace,
	}); err != nil {
		// No Hardware CRD installed, or no RBAC — the fallback still serves.
		return ""
	}
	for i := range hardware.Items {
		if image := hardware.Items[i].GetAnnotations()[AnnotationKey]; image != "" {
			return image
		}
	}
	return ""
}

// fromInfraMachineStatus is the transition fallback: the InfraMachine's
// status.installerImage, read generically.
func fromInfraMachineStatus(ctx context.Context, c client.Reader, namespace string, ref capiv1.ContractVersionedObjectReference) (string, error) {
	infra := &unstructured.Unstructured{}
	infra.SetGroupVersionKind(schema.GroupVersionKind{
		Group: ref.APIGroup,
		// The contract-versioned reference carries no version. Any served
		// version exposes the same status field, so the stored version
		// resolves it.
		Version: "v1beta2",
		Kind:    ref.Kind,
	})
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
	if err := c.Get(ctx, key, infra); err != nil {
		// Not being able to read the InfraMachine must not block bootstrap
		// generation, which is itself a prerequisite for provisioning it.
		return "", nil //nolint:nilerr // absence is the expected case
	}
	image, found, err := unstructured.NestedString(infra.Object, "status", "installerImage")
	if err != nil {
		return "", fmt.Errorf("reading %s status.installerImage: %w", ref.Kind, err)
	}
	if !found {
		return "", nil
	}
	return image, nil
}
