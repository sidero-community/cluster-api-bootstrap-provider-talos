// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/encoder"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/types/k8s"
	"gopkg.in/yaml.v3"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/configowner"
)

const (
	// JoinIDLabel is the label a kubelet registers its Node with when spec.restrictJoin is set.
	// Its value is the UID of the Machine or MachinePool that owns the TalosConfig, and the
	// join policy in the workload cluster only admits values of living owners.
	JoinIDLabel = "bootstrap.cluster.x-k8s.io/talos-join-id"

	// kubeletNodeLabelsArg is the kubelet argument whose labels are part of the Node the kubelet
	// creates. machine.nodeLabels will not do: Talos applies those after the Node exists.
	kubeletNodeLabelsArg = "node-labels"

	// joinTokenWorkerType is the only spec.generateType that gets a bootstrap token of its own.
	// A control plane configuration defines the cluster-wide token, and what a user-supplied
	// one (generateType none) is for cannot be told from the spec.
	joinTokenWorkerType = "worker"

	// joinTokenDerivation separates join tokens from anything else derived from the same key.
	joinTokenDerivation = "cluster-api-bootstrap-provider-talos/join-token/v1/"

	joinTokenIDLength     = 6
	joinTokenSecretLength = 16
)

// joinID returns the join ID of a TalosConfig owner.
func joinID(owner *configowner.ConfigOwner) string {
	return string(owner.GetUID())
}

// hasJoinToken reports whether the owner of config joins with a bootstrap token of its own.
// Members of a MachinePool share one rendered configuration, so they keep the cluster's.
func hasJoinToken(config *bootstrapv1beta1.TalosConfig, ownerIsMachinePool bool) bool {
	return config.Spec.RestrictJoin && config.Spec.GenerateType == joinTokenWorkerType && !ownerIsMachinePool
}

// joinToken derives the Kubernetes bootstrap token of the owner with the given UID.
//
// The token is a function of the cluster's Kubernetes CA key and the owner, so it is the same
// every time the configuration is rendered and the join gate can work it out without anything
// being stored. The CA key never reaches a worker, so holding one worker's token (or the
// cluster-wide one) does not give away another's.
func joinToken(bundle *secrets.Bundle, ownerUID string) (id, secret string, err error) {
	if bundle == nil || bundle.Certs == nil || bundle.Certs.K8s == nil || len(bundle.Certs.K8s.Key) == 0 {
		return "", "", errors.New("the cluster secrets hold no Kubernetes CA key to derive a join token from")
	}

	mac := hmac.New(sha256.New, bundle.Certs.K8s.Key)
	mac.Write([]byte(joinTokenDerivation + ownerUID))

	// A bootstrap token is [a-z0-9]{6}.[a-z0-9]{16}, which is what base 36 writes. The low
	// digits are taken: unlike the leading one they are evenly spread.
	const length = joinTokenIDLength + joinTokenSecretLength

	digits := new(big.Int).SetBytes(mac.Sum(nil)).Text(36)
	digits = strings.Repeat("0", max(0, length-len(digits))) + digits
	digits = digits[len(digits)-length:]

	return digits[:joinTokenIDLength], digits[joinTokenIDLength:], nil
}

// restrictJoin renders spec.restrictJoin into the machine configuration: the join ID as a label
// the kubelet registers with and, for a worker owned by a Machine, its own bootstrap token.
//
// It runs after every patch of the TalosConfig, so a patch cannot drop the label and leave the
// machine unable to join.
func (r *TalosConfigReconciler) restrictJoin(ctx context.Context, scope *TalosConfigScope, data string) (string, error) {
	if !scope.Config.Spec.RestrictJoin {
		return data, nil
	}

	cfg, err := configloader.NewFromBytes([]byte(data))
	if err != nil {
		return "", fmt.Errorf("failure loading the configuration to restrict joining: %w", err)
	}

	if cfg.Machine() == nil {
		return "", errors.New("spec.restrictJoin needs a machine configuration, and the rendered data has none")
	}

	// Labels the configuration already asks the kubelet for are kept, apart from a join ID.
	labels := []string{}

	if kubelet := cfg.K8sKubeletConfig(); kubelet != nil {
		for _, value := range kubelet.ExtraArgs()[kubeletNodeLabelsArg] {
			for label := range strings.SplitSeq(value, ",") {
				if label = strings.TrimSpace(label); label != "" && !strings.HasPrefix(label, JoinIDLabel+"=") {
					labels = append(labels, label)
				}
			}
		}
	}

	labels = append(labels, JoinIDLabel+"="+joinID(scope.ConfigOwner))

	extraArgs := map[string]any{kubeletNodeLabelsArg: strings.Join(labels, ",")}

	// The kubelet's arguments live in a KubeletConfig document when the configuration has one,
	// and in the legacy machine.kubelet block otherwise.
	documents := []map[string]any{}
	legacy := map[string]any{}

	if hasDocument(cfg, k8s.KubeletConfig) {
		documents = append(documents, map[string]any{
			"apiVersion": "v1alpha1",
			"kind":       k8s.KubeletConfig,
			"extraArgs":  extraArgs,
		})
	} else {
		legacy["machine"] = map[string]any{"kubelet": map[string]any{"extraArgs": extraArgs}}
	}

	if hasJoinToken(scope.Config, scope.ConfigOwner.IsMachinePool()) {
		contract, err := versionContractFor(scope.Config.Spec.TalosVersion)
		if err != nil {
			return "", err
		}

		bundle, err := r.getSecretsBundle(ctx, scope, false, contract)
		if err != nil {
			return "", err
		}

		id, secret, err := joinToken(bundle, joinID(scope.ConfigOwner))
		if err != nil {
			return "", err
		}

		legacy["cluster"] = map[string]any{"token": id + "." + secret}
	}

	if len(legacy) > 0 {
		documents = append(documents, legacy)
	}

	patches := make([]configpatcher.Patch, 0, len(documents))

	for _, document := range documents {
		marshalled, err := yaml.Marshal(document)
		if err != nil {
			return "", err
		}

		loaded, err := configpatcher.LoadPatch(marshalled)
		if err != nil {
			return "", fmt.Errorf("failure loading the join restriction patch: %w", err)
		}

		patches = append(patches, loaded)
	}

	out, err := configpatcher.Apply(configpatcher.WithBytes([]byte(data)), patches)
	if err != nil {
		return "", fmt.Errorf("failure applying the join restriction: %w", err)
	}

	outCfg, err := out.Config()
	if err != nil {
		return "", fmt.Errorf("failure converting result to bytes: %w", err)
	}

	return outCfg.EncodeString(encoder.WithComments(encoder.CommentsDisabled))
}

// hasDocument reports whether cfg carries a configuration document of the given kind.
func hasDocument(cfg config.Provider, kind string) bool {
	for _, document := range cfg.Documents() {
		if document.Kind() == kind {
			return true
		}
	}

	return false
}
