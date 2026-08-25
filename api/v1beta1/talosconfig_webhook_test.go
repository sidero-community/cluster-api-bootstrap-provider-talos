// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

// updateContext carries a non-dry-run admission request, which is what ValidateUpdate reads
// to distinguish a real write from a topology controller dry-run.
func updateContext() context.Context {
	return admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
		},
	})
}

func config(annotations map[string]string, spec bootstrapv1beta1.TalosConfigSpec) *bootstrapv1beta1.TalosConfig {
	return &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test",
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: spec,
	}
}

func TestValidateUpdate_SpecIsImmutableByDefault(t *testing.T) {
	t.Parallel()

	old := config(nil, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.12"})
	updated := config(nil, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"})

	_, err := updated.ValidateUpdate(updateContext(), old, updated)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
}

// The owning controller stamps the annotation and the desired spec in the same admission
// request, so an in-place update is admitted while immutability holds for everyone else.
func TestValidateUpdate_SpecChangeAdmittedDuringInPlaceUpdate(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{clusterv1.UpdateInProgressAnnotation: ""}

	old := config(nil, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.12"})
	updated := config(annotations, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"})

	_, err := updated.ValidateUpdate(updateContext(), old, updated)

	require.NoError(t, err)
}

func TestValidateUpdate_UnchangedSpecIsAlwaysAdmitted(t *testing.T) {
	t.Parallel()

	spec := bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"}

	_, err := config(nil, spec).ValidateUpdate(updateContext(), config(nil, spec), config(nil, spec))

	require.NoError(t, err)
}

// Exempting the immutability check must not exempt the rest of validation.
func TestValidateUpdate_StillValidatesDuringInPlaceUpdate(t *testing.T) {
	t.Parallel()

	annotations := map[string]string{clusterv1.UpdateInProgressAnnotation: ""}

	old := config(nil, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker"})
	updated := config(annotations, bootstrapv1beta1.TalosConfigSpec{
		GenerateType: "worker",
		Hostname:     bootstrapv1beta1.HostnameSpec{Source: "NotARealSource"},
	})

	_, err := updated.ValidateUpdate(updateContext(), old, updated)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostname")
}

func TestIsInPlaceUpdate(t *testing.T) {
	t.Parallel()

	assert.False(t, bootstrapv1beta1.IsInPlaceUpdate(config(nil, bootstrapv1beta1.TalosConfigSpec{})))
	assert.False(t, bootstrapv1beta1.IsInPlaceUpdate(config(map[string]string{"other": "x"}, bootstrapv1beta1.TalosConfigSpec{})))
	assert.True(t, bootstrapv1beta1.IsInPlaceUpdate(
		config(map[string]string{clusterv1.UpdateInProgressAnnotation: ""}, bootstrapv1beta1.TalosConfigSpec{}),
	))
}

// The hash guards against acting on a stale bootstrap data secret, so it has to move when
// either input moves. The Kubernetes version matters because a version bump changes the
// rendered config while leaving the TalosConfig spec untouched.
func TestInPlaceConfigHash(t *testing.T) {
	t.Parallel()

	spec := bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.13"}

	base, err := bootstrapv1beta1.InPlaceConfigHash(spec, "1.34.0", "")
	require.NoError(t, err)

	same, err := bootstrapv1beta1.InPlaceConfigHash(spec, "1.34.0", "")
	require.NoError(t, err)
	assert.Equal(t, base, same, "hash must be stable for identical inputs")

	otherVersion, err := bootstrapv1beta1.InPlaceConfigHash(spec, "1.35.0", "")
	require.NoError(t, err)
	assert.NotEqual(t, base, otherVersion, "a Kubernetes version bump must change the hash")

	otherSpec, err := bootstrapv1beta1.InPlaceConfigHash(
		bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.12"},
		"1.34.0",
		"",
	)
	require.NoError(t, err)
	assert.NotEqual(t, base, otherSpec, "a spec change must change the hash")

	// The installer image is injected into the rendered config by the infrastructure provider
	// without appearing in the spec, so it has to move the hash too.
	otherImage, err := bootstrapv1beta1.InPlaceConfigHash(spec, "1.34.0",
		"factory.talos.dev/metal-installer/abc:v1.14.0")
	require.NoError(t, err)
	assert.NotEqual(t, base, otherImage, "an installer image change must change the hash")
}
