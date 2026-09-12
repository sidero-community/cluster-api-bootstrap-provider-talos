// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"

	bootstrapv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

func imageFactorySpec() *bootstrapv1beta1.ImageFactorySpec {
	return &bootstrapv1beta1.ImageFactorySpec{
		Extensions:      []string{"siderolabs/nvme-cli", "siderolabs/intel-ucode"},
		ExtraKernelArgs: []string{"talos.logging.kernel=udp://10.0.0.5:514/"},
		Overlay:         &bootstrapv1beta1.ImageFactoryOverlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
		Bootloader:      "sd-boot",
	}
}

// v1alpha3 has no imageFactory, so a round trip through it must stash and restore both the
// spec block and the resolved status block.
func TestTalosConfigRoundTripPreservesImageFactory(t *testing.T) {
	t.Parallel()

	hub := &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: "v1.14", ImageFactory: imageFactorySpec()},
		Status: bootstrapv1beta1.TalosConfigStatus{ImageFactory: &bootstrapv1beta1.ImageFactoryStatus{
			TalosVersion: "v1.14.2", SchematicID: "abc", InstallerImage: "factory.talos.dev/metal-installer/abc:v1.14.2", ObservedInputs: "deadbeef",
		}},
	}

	spoke := &bootstrapv1alpha3.TalosConfig{}
	require.NoError(t, spoke.ConvertFrom(hub))
	assert.Contains(t, spoke.Annotations, utilconversion.DataAnnotation)

	restored := &bootstrapv1beta1.TalosConfig{}
	require.NoError(t, spoke.ConvertTo(restored))

	assert.Equal(t, hub.Spec, restored.Spec)
	assert.Equal(t, hub.Status.ImageFactory, restored.Status.ImageFactory)
}

func TestTalosConfigTemplateRoundTripPreservesImageFactory(t *testing.T) {
	t.Parallel()

	hub := templateHub()
	hub.Spec.Template.Spec.ImageFactory = imageFactorySpec()

	spoke := &bootstrapv1alpha3.TalosConfigTemplate{}
	require.NoError(t, spoke.ConvertFrom(hub))

	restored := &bootstrapv1beta1.TalosConfigTemplate{}
	require.NoError(t, spoke.ConvertTo(restored))

	assert.Equal(t, hub.Spec, restored.Spec)
}
