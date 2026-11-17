// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// joinGateKubeconfigEnv names a kubeconfig of a disposable cluster (kind will do) to try the
// join gate against. The policy is a CEL expression evaluated by a real API server next to the
// NodeRestriction admission plugin, which no fake can stand in for.
//
//	kind create cluster --name join-gate --kubeconfig /tmp/join-gate.kubeconfig
//	JOIN_GATE_KUBECONFIG=/tmp/join-gate.kubeconfig go test ./controllers/ -run TestJoinGateAgainstACluster -v
const joinGateKubeconfigEnv = "JOIN_GATE_KUBECONFIG"

func TestJoinGateAgainstACluster(t *testing.T) {
	kubeconfig := os.Getenv(joinGateKubeconfigEnv)
	if kubeconfig == "" {
		t.Skip(joinGateKubeconfigEnv + " is not set")
	}

	ctx := context.Background()

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err)

	admin, err := client.New(config, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	// kubelet returns a client that is the kubelet of the named node.
	kubelet := func(node string) client.Client {
		impersonated := rest.CopyConfig(config)
		impersonated.Impersonate = rest.ImpersonationConfig{
			UserName: "system:node:" + node,
			Groups:   []string{"system:nodes", "system:authenticated"},
		}

		c, err := client.New(impersonated, client.Options{Scheme: scheme.Scheme})
		require.NoError(t, err)

		return c
	}

	node := func(name, joinID string) *corev1.Node {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if joinID != "" {
			n.Labels = map[string]string{JoinIDLabel: joinID}
		}

		return n
	}

	const tokenID, tokenSecret = "jgtest", "0123456789abcdef"

	names := []string{"jg-living", "jg-stale", "jg-unlabelled", "jg-known", "jg-by-admin", "jg-warned"}

	cleanup := func() {
		for _, name := range names {
			_ = admin.Delete(ctx, node(name, ""))
		}

		require.NoError(t, removeJoinGate(ctx, admin))
	}

	cleanup()
	t.Cleanup(cleanup)

	gate := &joinGate{
		restricted: true,
		allowlist: map[string]string{
			joinAllowlistIDPrefix + "living":     "Machine living",
			joinAllowlistNamePrefix + "jg-known": "Machine known",
		},
		tokens: map[string]joinGateToken{tokenID: {joinID: "living", secret: tokenSecret, owner: "Machine living"}},
	}

	require.NoError(t, applyJoinGate(ctx, admin, gate))

	// The API server takes a moment to start enforcing a new policy.
	require.Eventually(t, func() bool {
		err := kubelet("jg-stale").Create(ctx, node("jg-stale", "stale"))
		if err == nil {
			_ = admin.Delete(ctx, node("jg-stale", ""))
		}

		return apierrors.IsForbidden(err)
	}, time.Minute, time.Second, "a kubelet with the join ID of a deleted Machine must be refused")

	err = kubelet("jg-stale").Create(ctx, node("jg-stale", "stale"))
	require.True(t, apierrors.IsForbidden(err), "got %v", err)
	assert.Contains(t, err.Error(), "does not belong to a living Cluster API Machine")

	err = kubelet("jg-unlabelled").Create(ctx, node("jg-unlabelled", ""))
	assert.True(t, apierrors.IsForbidden(err), "a kubelet without a join ID must be refused, got %v", err)

	assert.NoError(t, kubelet("jg-living").Create(ctx, node("jg-living", "living")), "a living Machine's kubelet registers, and NodeRestriction lets it set the label")
	assert.NoError(t, kubelet("jg-known").Create(ctx, node("jg-known", "")), "a Node the allowlist names registers without a join ID")
	assert.NoError(t, admin.Create(ctx, node("jg-by-admin", "")), "the policy is about kubelets only")

	// The token signs in as a node bootstrapper while it exists, and stops when it is removed.
	whoami := func() (*authenticationv1.SelfSubjectReview, error) {
		bearer := rest.AnonymousClientConfig(config)
		bearer.BearerToken = tokenID + "." + tokenSecret

		c, err := client.New(bearer, client.Options{Scheme: scheme.Scheme})
		require.NoError(t, err)

		review := &authenticationv1.SelfSubjectReview{}

		return review, c.Create(ctx, review)
	}

	var review *authenticationv1.SelfSubjectReview

	require.Eventually(t, func() bool {
		review, err = whoami()

		return err == nil
	}, time.Minute, time.Second, "the join token must authenticate: %v", err)

	assert.Equal(t, "system:bootstrap:"+tokenID, review.Status.UserInfo.Username)
	assert.Contains(t, review.Status.UserInfo.Groups, "system:bootstrappers:nodes")

	// A machine the allowlist cannot describe turns the policy from refusing to warning.
	gate.tokens = nil
	gate.uncovered = []string{"Machine cp-1"}

	require.NoError(t, applyJoinGate(ctx, admin, gate))

	require.Eventually(t, func() bool {
		return kubelet("jg-warned").Create(ctx, node("jg-warned", "stale")) == nil
	}, time.Minute, time.Second, "while only warning the policy must admit every kubelet")

	require.Eventually(t, func() bool {
		_, err = whoami()

		return apierrors.IsUnauthorized(err)
	}, time.Minute, time.Second, "a removed join token must stop authenticating, got %v", err)
}
