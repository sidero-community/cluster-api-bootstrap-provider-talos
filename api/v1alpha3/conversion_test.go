// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"

	bootstrapv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

func templateHub() *bootstrapv1beta1.TalosConfigTemplate {
	return &bootstrapv1beta1.TalosConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Spec: bootstrapv1beta1.TalosConfigTemplateSpec{
			Template: bootstrapv1beta1.TalosConfigTemplateResource{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels:      map[string]string{"example.com/pool": "workers"},
					Annotations: map[string]string{"example.com/note": "keep me"},
				},
				Spec: bootstrapv1beta1.TalosConfigSpec{
					GenerateType: "worker",
					TalosVersion: "v1.13",
				},
			},
		},
	}
}

// v1alpha3 has no spec.template.metadata, so a round trip through it drops the field unless the
// hub data is stashed on the way down and restored on the way up.
func TestTalosConfigTemplateRoundTripPreservesTemplateMetadata(t *testing.T) {
	t.Parallel()

	hub := templateHub()

	spoke := &bootstrapv1alpha3.TalosConfigTemplate{}
	require.NoError(t, spoke.ConvertFrom(hub))
	assert.Contains(t, spoke.Annotations, utilconversion.DataAnnotation, "hub data must be stashed for the way back")

	restored := &bootstrapv1beta1.TalosConfigTemplate{}
	require.NoError(t, spoke.ConvertTo(restored))

	assert.Equal(t, hub.Spec, restored.Spec)
	assert.NotContains(t, restored.Annotations, utilconversion.DataAnnotation, "the stash must not leak into the restored object")
}

// A v1alpha3 object written by an old client carries no stashed hub data; converting it up must
// still succeed and simply leave the template metadata empty.
func TestTalosConfigTemplateConvertToWithoutStashedData(t *testing.T) {
	t.Parallel()

	spoke := &bootstrapv1alpha3.TalosConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Spec: bootstrapv1alpha3.TalosConfigTemplateSpec{
			Template: bootstrapv1alpha3.TalosConfigTemplateResource{
				Spec: bootstrapv1alpha3.TalosConfigSpec{
					GenerateType: "worker",
					TalosVersion: "v1.13",
				},
			},
		},
	}

	hub := &bootstrapv1beta1.TalosConfigTemplate{}
	require.NoError(t, spoke.ConvertTo(hub))

	assert.Equal(t, clusterv1.ObjectMeta{}, hub.Spec.Template.ObjectMeta)
	assert.Equal(t, "worker", hub.Spec.Template.Spec.GenerateType)
}
