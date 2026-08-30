// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"

	bootstrapv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

// The manager builds its client from this shared scheme, so a type missing here fails only at
// runtime with "no kind is registered for the type". A test that constructs its own scheme
// cannot catch that, which is exactly how the in-place update registrar shipped unable to read
// an ExtensionConfig.
func TestSharedSchemeKnowsEveryTypeTheManagerUses(t *testing.T) {
	t.Parallel()

	for name, obj := range map[string]runtime.Object{
		"TalosConfig v1beta1":  &bootstrapv1beta1.TalosConfig{},
		"TalosConfig v1alpha3": &bootstrapv1alpha3.TalosConfig{},
		"Machine":              &capiv1.Machine{},
		"ExtensionConfig":      &runtimev1.ExtensionConfig{},
	} {
		gvks, _, err := scheme.Scheme.ObjectKinds(obj)
		require.NoErrorf(t, err, "%s is not registered in the shared scheme", name)
		require.NotEmptyf(t, gvks, "%s resolved to no GroupVersionKind", name)
	}
}
