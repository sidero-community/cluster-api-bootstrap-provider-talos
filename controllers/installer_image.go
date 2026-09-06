// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/installerimage"
)

// installerImageFor reads the installer image resolved for a machine, or an
// empty string when there is none yet.
//
// The source is the per-machine talos.tinkerbell.org/installer-image Hardware
// annotation published by the talos-image-resolver runtime extension, with the
// InfraMachine status.installerImage read kept as a transition fallback — see
// internal/installerimage. The in-place update handler recomputes the config
// hash through the same function, so writer and reader can never disagree on
// the source.
func installerImageFor(ctx context.Context, c client.Client, owner *unstructured.Unstructured) (string, error) {
	if owner == nil {
		return "", nil
	}

	machine := &capiv1.Machine{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(owner.Object, machine); err != nil {
		return "", fmt.Errorf("decoding config owner as a Machine: %w", err)
	}

	return installerimage.Resolve(ctx, c, machine)
}

// installImagePatch renders a strategic merge patch pinning the Talos installer image.
//
// It is applied ahead of any user-supplied strategic patches so that an explicit patch in the
// TalosConfig still wins. The resolved image is a good default, not an override of intent.
func installImagePatch(image string) string {
	return fmt.Sprintf("machine:\n  install:\n    image: %s\n", image)
}
