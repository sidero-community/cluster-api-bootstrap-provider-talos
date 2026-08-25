// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// InPlaceConfigHashAnnotation is set by CABPT on the bootstrap data secret and records a
// hash of the inputs the machine configuration was rendered from.
//
// The in-place update extension uses it to tell a freshly regenerated secret from a stale
// one. Without it the extension could read the pre-update configuration, apply it as a
// no-op, and report the in-place update complete without having changed anything.
const InPlaceConfigHashAnnotation = "bootstrap.cluster.x-k8s.io/in-place-config-hash"

// IsInPlaceUpdate reports whether an object is being written as part of a Cluster API
// in-place update, i.e. whether it carries clusterv1.UpdateInProgressAnnotation.
//
// The annotation is stamped by the controller that owns the Machine (TalosControlPlane for
// control planes, MachineSet for workers) before it writes the desired spec, and removed by
// the core Machine controller once the UpdateMachine hook reports completion.
func IsInPlaceUpdate(obj metav1.Object) bool {
	_, ok := obj.GetAnnotations()[clusterv1.UpdateInProgressAnnotation]

	return ok
}

// inPlaceConfigInputs captures everything outside the cluster's PKI that determines a
// rendered Talos machine configuration and can legitimately change during an in-place
// update. Field order is fixed, and encoding/json emits struct fields in declaration
// order, so the marshalled form is stable across processes.
type inPlaceConfigInputs struct {
	Spec              TalosConfigSpec `json:"spec"`
	KubernetesVersion string          `json:"kubernetesVersion"`
	InstallerImage    string          `json:"installerImage"`
}

// InPlaceConfigHash returns a stable hash of the inputs that determine the rendered Talos
// machine configuration for a Machine.
//
// It deliberately covers more than the TalosConfig spec. A Kubernetes version bump changes
// the rendered configuration (the version feeds the generated control plane component and
// kubelet images) while leaving the spec untouched, and the installer image resolved by the
// infrastructure provider is injected into the configuration without appearing in the spec
// at all. Hashing the spec alone would let a stale secret masquerade as fresh in either case.
//
// The cluster PKI is intentionally excluded. It is stable for the life of the cluster and
// is not something an in-place update can change.
func InPlaceConfigHash(spec TalosConfigSpec, kubernetesVersion, installerImage string) (string, error) {
	encoded, err := json.Marshal(inPlaceConfigInputs{
		Spec:              spec,
		KubernetesVersion: kubernetesVersion,
		InstallerImage:    installerImage,
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode in-place config inputs: %w", err)
	}

	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:]), nil
}
