// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func registrarFixture(t *testing.T, objs ...client.Object) (*Registrar, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, runtimev1.AddToScheme(scheme))

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("THE-CA"), 0o600))

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	return NewRegistrar(c, RegistrarOptions{
		Name:             "cabpt-talos-in-place-updates",
		ServiceName:      "cabpt-runtime-extension-service",
		ServiceNamespace: "talos-bootstrap-system",
		ServicePort:      443,
		CACertPath:       filepath.Join(dir, "ca.crt"),
	}), c
}

func readExtensionConfig(t *testing.T, c client.Client) *runtimev1.ExtensionConfig {
	t.Helper()

	ec := &runtimev1.ExtensionConfig{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cabpt-talos-in-place-updates"}, ec))

	return ec
}

func TestRegistrarCreatesExtensionConfig(t *testing.T) {
	t.Parallel()

	r, c := registrarFixture(t)

	require.NoError(t, r.ensure(context.Background()))

	ec := readExtensionConfig(t, c)
	require.Equal(t, "cabpt-runtime-extension-service", ec.Spec.ClientConfig.Service.Name)
	require.Equal(t, "talos-bootstrap-system", ec.Spec.ClientConfig.Service.Namespace)
	require.Equal(t, "THE-CA", string(ec.Spec.ClientConfig.CABundle))
}

// The failure that motivated writing this: a manifest-shipped ExtensionConfig names the
// namespace it was built for, not the one it was installed into, and carries no CA because
// cert-manager cannot inject one. Cluster API treats that as fatal and crash-loops, so the
// registrar has to repair an existing object rather than only create a missing one.
func TestRegistrarCorrectsWrongNamespaceAndMissingCA(t *testing.T) {
	t.Parallel()

	stale := &runtimev1.ExtensionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cabpt-talos-in-place-updates"},
		Spec: runtimev1.ExtensionConfigSpec{
			ClientConfig: runtimev1.ClientConfig{
				Service: runtimev1.ServiceReference{
					Name:      "cabpt-runtime-extension-service",
					Namespace: "cabpt-system",
				},
			},
		},
	}

	r, c := registrarFixture(t, stale)

	require.NoError(t, r.ensure(context.Background()))

	ec := readExtensionConfig(t, c)
	require.Equal(t, "talos-bootstrap-system", ec.Spec.ClientConfig.Service.Namespace)
	require.Equal(t, "THE-CA", string(ec.Spec.ClientConfig.CABundle))
}

func TestRegistrarPreservesNamespaceSelector(t *testing.T) {
	t.Parallel()

	narrowed := &runtimev1.ExtensionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cabpt-talos-in-place-updates"},
		Spec: runtimev1.ExtensionConfigSpec{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"only": "here"},
			},
		},
	}

	r, c := registrarFixture(t, narrowed)

	require.NoError(t, r.ensure(context.Background()))

	ec := readExtensionConfig(t, c)
	require.NotNil(t, ec.Spec.NamespaceSelector)
	require.Equal(t, map[string]string{"only": "here"}, ec.Spec.NamespaceSelector.MatchLabels)
}

func TestRegistrarFailsOnMissingCA(t *testing.T) {
	t.Parallel()

	r, _ := registrarFixture(t)
	r.opts.CACertPath = filepath.Join(t.TempDir(), "absent.crt")

	require.Error(t, r.ensure(context.Background()))
}
