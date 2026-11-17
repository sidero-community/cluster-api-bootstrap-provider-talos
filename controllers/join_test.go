// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

// The fixture's Machine has this UID, so it is the join ID of everything rendered for it.
const fixtureJoinLabel = JoinIDLabel + "=owner-uid"

func renderedConfig(t *testing.T, f *imageFactoryFixture) config.Provider {
	t.Helper()

	require.NoError(t, f.reconcile(t))

	provider, err := configloader.NewFromBytes([]byte(f.bootstrapData(t)))
	require.NoError(t, err)

	return provider
}

func clusterBundle(t *testing.T, f *imageFactoryFixture) *secrets.Bundle {
	t.Helper()

	bundle := &secrets.Bundle{}
	require.NoError(t, yaml.Unmarshal(f.secret(t, testClusterName+"-talos").Data["bundle"], bundle))

	return bundle
}

func renderedToken(provider config.Provider) string {
	return provider.Cluster().Token().ID() + "." + provider.Cluster().Token().Secret()
}

func TestJoinTokenIsAStableBootstrapTokenPerOwner(t *testing.T) {
	t.Parallel()

	bundle, err := secrets.NewBundle(secrets.NewClock(), config.TalosVersionCurrent)
	require.NoError(t, err)

	other, err := secrets.NewBundle(secrets.NewClock(), config.TalosVersionCurrent)
	require.NoError(t, err)

	id, secret, err := joinToken(bundle, "machine-a")
	require.NoError(t, err)

	assert.Regexp(t, regexp.MustCompile(`^[a-z0-9]{6}$`), id)
	assert.Regexp(t, regexp.MustCompile(`^[a-z0-9]{16}$`), secret)

	againID, againSecret, err := joinToken(bundle, "machine-a")
	require.NoError(t, err)
	assert.Equal(t, id+"."+secret, againID+"."+againSecret, "rendering twice must give the same token")

	otherID, otherSecret, err := joinToken(bundle, "machine-b")
	require.NoError(t, err)
	assert.NotEqual(t, id, otherID)
	assert.NotEqual(t, secret, otherSecret)

	_, foreignSecret, err := joinToken(other, "machine-a")
	require.NoError(t, err)
	assert.NotEqual(t, secret, foreignSecret, "another cluster must not derive the same token")

	_, _, err = joinToken(&secrets.Bundle{}, "machine-a")
	require.Error(t, err)
}

func TestReconcileLeavesJoiningAloneByDefault(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker"}, nil)
	provider := renderedConfig(t, f)

	assert.Empty(t, provider.K8sKubeletConfig().ExtraArgs()["node-labels"])

	bundle := clusterBundle(t, f)
	assert.Equal(t, bundle.Secrets.BootstrapToken, renderedToken(provider))
}

func TestReconcileRestrictJoinGivesAWorkerItsOwnIdentity(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"v1.11", "v1.14", ""} {
		t.Run("version="+version, func(t *testing.T) {
			t.Parallel()

			f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "worker", TalosVersion: version, RestrictJoin: true}, nil)
			provider := renderedConfig(t, f)

			assert.Equal(t, []string{fixtureJoinLabel}, provider.K8sKubeletConfig().ExtraArgs()["node-labels"])

			bundle := clusterBundle(t, f)

			id, secret, err := joinToken(bundle, "owner-uid")
			require.NoError(t, err)

			assert.Equal(t, id+"."+secret, renderedToken(provider))
			assert.NotEqual(t, bundle.Secrets.BootstrapToken, renderedToken(provider), "a restricted worker must not hold the cluster-wide token")
		})
	}
}

func TestReconcileRestrictJoinKeepsTheControlPlaneToken(t *testing.T) {
	t.Parallel()

	f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "controlplane", RestrictJoin: true}, nil)
	provider := renderedConfig(t, f)

	assert.Equal(t, []string{fixtureJoinLabel}, provider.K8sKubeletConfig().ExtraArgs()["node-labels"])

	// A control plane node publishes its token as the cluster's bootstrap token Secret, so it
	// has to stay the one every other control plane node renders.
	assert.Equal(t, clusterBundle(t, f).Secrets.BootstrapToken, renderedToken(provider))
}

func TestReconcileRestrictJoinKeepsLabelsFromPatches(t *testing.T) {
	t.Parallel()

	const args = "node-labels: example.com/pool=gpu," + JoinIDLabel + "=forged\n%[1]srotate-server-certificates: \"true\"\n"

	for _, tc := range []struct {
		version string
		patch   string
	}{
		// Before 1.14 the kubelet's arguments are part of the machine block.
		{version: "v1.11", patch: "machine:\n  kubelet:\n    extraArgs:\n      " + fmt.Sprintf(args, "      ")},
		// From 1.14 they are a document of their own.
		{version: "v1.14", patch: "apiVersion: v1alpha1\nkind: KubeletConfig\nextraArgs:\n  " + fmt.Sprintf(args, "  ")},
	} {
		t.Run("version="+tc.version, func(t *testing.T) {
			t.Parallel()

			f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{
				GenerateType:     "worker",
				TalosVersion:     tc.version,
				RestrictJoin:     true,
				StrategicPatches: []string{tc.patch},
			}, nil)
			provider := renderedConfig(t, f)

			args := provider.K8sKubeletConfig().ExtraArgs()

			assert.Equal(t, []string{"example.com/pool=gpu," + fixtureJoinLabel}, args["node-labels"], "a patch may add labels but not choose the join ID")
			assert.Equal(t, []string{"true"}, args["rotate-server-certificates"], "other kubelet arguments must survive")
		})
	}
}

func TestReconcileRestrictJoinAppliesToUserSuppliedConfig(t *testing.T) {
	t.Parallel()

	data, bundle := userSuppliedConfig(t, config.TalosVersionCurrent)

	f := newImageFactoryFixture(t, bootstrapv1beta1.TalosConfigSpec{GenerateType: "none", Data: data, RestrictJoin: true}, nil)
	provider := renderedConfig(t, f)

	assert.Equal(t, []string{fixtureJoinLabel}, provider.K8sKubeletConfig().ExtraArgs()["node-labels"])
	assert.Equal(t, bundle.Secrets.BootstrapToken, renderedToken(provider), "what a user-supplied configuration joins with is the user's to say")
}
