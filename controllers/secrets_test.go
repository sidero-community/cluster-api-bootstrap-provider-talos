// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	bsutil "sigs.k8s.io/cluster-api/bootstrap/util"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

const bootstrapSecretName = "machine-1-bootstrap-data"

// newSecretsScope builds a reconciler and scope for a Machine named machine-1, optionally
// with an existing bootstrap data secret.
func newSecretsScope(t *testing.T, inPlace bool, existing *corev1.Secret) (*TalosConfigReconciler, *TalosConfigScope) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, capiv1.AddToScheme(scheme))
	require.NoError(t, bootstrapv1beta1.AddToScheme(scheme))

	builder := fake.NewClientBuilder().WithScheme(scheme)
	if existing != nil {
		builder = builder.WithObjects(existing)
	}

	annotations := map[string]string{}
	if inPlace {
		annotations[capiv1.UpdateInProgressAnnotation] = ""
	}

	config := &bootstrapv1beta1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "machine-1",
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker"},
	}

	machine := &capiv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-1", Namespace: "default"},
		Spec:       capiv1.MachineSpec{ClusterName: "test", Version: "v1.34.0"},
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(machine)
	require.NoError(t, err)

	owned := &unstructured.Unstructured{Object: raw}
	owned.SetAPIVersion(capiv1.GroupVersion.String())
	owned.SetKind("Machine")

	owner := &bsutil.ConfigOwner{Unstructured: owned}

	return &TalosConfigReconciler{
			Client: builder.Build(),
			Log:    ctrl.Log.WithName("test"),
			Scheme: scheme,
		}, &TalosConfigScope{
			Config:      config,
			ConfigOwner: owner,
			Cluster:     &capiv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}},
		}
}

func existingSecret(data, hash string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: bootstrapSecretName},
		Data:       map[string][]byte{"value": []byte(data)},
	}

	if hash != "" {
		secret.Annotations = map[string]string{bootstrapv1beta1.InPlaceConfigHashAnnotation: hash}
	}

	return secret
}

func readSecret(t *testing.T, r *TalosConfigReconciler, scope *TalosConfigScope) *corev1.Secret {
	t.Helper()

	secret, err := r.fetchSecret(context.Background(), scope.Config, bootstrapSecretName)
	require.NoError(t, err)

	return secret
}

func TestWriteBootstrapData_CreatesSecretWithHash(t *testing.T) {
	t.Parallel()

	r, scope := newSecretsScope(t, false, nil)

	name, err := r.writeBootstrapData(context.Background(), scope, []byte("config-v1"), "hash-1")
	require.NoError(t, err)
	assert.Equal(t, bootstrapSecretName, name)

	secret := readSecret(t, r, scope)
	assert.Equal(t, "config-v1", string(secret.Data["value"]))
	assert.Equal(t, "hash-1", secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation])
}

// Bootstrap data is immutable for the life of a Machine outside an in-place update.
func TestWriteBootstrapData_LeavesExistingSecretAlone(t *testing.T) {
	t.Parallel()

	r, scope := newSecretsScope(t, false, existingSecret("config-v1", "hash-1"))

	_, err := r.writeBootstrapData(context.Background(), scope, []byte("config-v2"), "hash-2")
	require.NoError(t, err)

	secret := readSecret(t, r, scope)
	assert.Equal(t, "config-v1", string(secret.Data["value"]), "must not rewrite outside an in-place update")
	assert.Equal(t, "hash-1", secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation])
}

func TestWriteBootstrapData_RegeneratesDuringInPlaceUpdate(t *testing.T) {
	t.Parallel()

	r, scope := newSecretsScope(t, true, existingSecret("config-v1", "hash-1"))

	_, err := r.writeBootstrapData(context.Background(), scope, []byte("config-v2"), "hash-2")
	require.NoError(t, err)

	secret := readSecret(t, r, scope)
	assert.Equal(t, "config-v2", string(secret.Data["value"]))
	assert.Equal(t, "hash-2", secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation],
		"the new hash is what tells the extension the secret is fresh")
}

// The update runs for many reconciles while the node settles; rewriting an already-current
// secret each time would churn its resourceVersion for no reason.
func TestWriteBootstrapData_SkipsRewriteWhenHashAlreadyMatches(t *testing.T) {
	t.Parallel()

	r, scope := newSecretsScope(t, true, existingSecret("config-v2", "hash-2"))

	before := readSecret(t, r, scope).ResourceVersion

	_, err := r.writeBootstrapData(context.Background(), scope, []byte("config-v2"), "hash-2")
	require.NoError(t, err)

	assert.Equal(t, before, readSecret(t, r, scope).ResourceVersion)
}
