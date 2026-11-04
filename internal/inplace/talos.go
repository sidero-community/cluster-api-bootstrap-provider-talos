// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"
	"strings"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeClient is the slice of the Talos machine API that an in-place update needs.
//
// It exists as an interface so the update state machine can be exercised without hardware:
// the real implementation talks gRPC to a node, and tests substitute a fake. Everything
// that reboots a machine sits behind this boundary.
type NodeClient interface {
	// Version returns the Talos version tag the node is currently running, e.g. "v1.13.0".
	Version(ctx context.Context) (string, error)

	// ApplyConfig applies a machine configuration in AUTO mode, which lets Talos decide
	// whether the change requires a reboot.
	ApplyConfig(ctx context.Context, data []byte) error

	// Upgrade drives a lifecycle upgrade to image: pull, install and reboot. It blocks for as
	// long as the install takes and returns once the reboot has been requested. Talos cordons
	// and drains the node itself as part of the reboot.
	Upgrade(ctx context.Context, image string) error

	// LegacyUpgrade asks a node that predates the lifecycle service (Talos before 1.13) to
	// upgrade itself through the machine service, and returns as soon as it has been asked.
	LegacyUpgrade(ctx context.Context, image string) error

	// APIServer observes the node's kube-apiserver: the static pod Talos rendered for it and
	// the kubelet's status of the pod running from it. A node without one, a worker, reports
	// an empty state.
	APIServer(ctx context.Context) (APIServerState, error)

	// Close releases the underlying connection.
	Close() error
}

// NodeClientFactory opens a NodeClient for the given machine.
type NodeClientFactory func(ctx context.Context, clusterKey types.NamespacedName, endpoints []string) (NodeClient, error)

// talosNodeClient is the gRPC-backed NodeClient.
type talosNodeClient struct {
	client  *talosclient.Client
	upgrade upgradeClients
}

// NewNodeClient builds a NodeClient for a machine using the cluster's talosconfig secret,
// which CABPT maintains as <cluster>-talosconfig.
func NewNodeClient(c client.Client) NodeClientFactory {
	return func(ctx context.Context, clusterKey types.NamespacedName, endpoints []string) (NodeClient, error) {
		if len(endpoints) == 0 {
			return nil, fmt.Errorf("machine has no reachable addresses")
		}

		var secret corev1.Secret

		key := types.NamespacedName{
			Namespace: clusterKey.Namespace,
			Name:      clusterKey.Name + "-talosconfig",
		}

		if err := c.Get(ctx, key, &secret); err != nil {
			return nil, fmt.Errorf("failed to read talosconfig secret %s: %w", key, err)
		}

		cfg, err := talosconfig.FromBytes(secret.Data["talosconfig"])
		if err != nil {
			return nil, fmt.Errorf("failed to parse talosconfig from secret %s: %w", key, err)
		}

		tc, err := talosclient.New(ctx,
			talosclient.WithDefaultGRPCDialOptions(),
			talosclient.WithEndpoints(endpoints...),
			talosclient.WithConfig(cfg),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to Talos API: %w", err)
		}

		return &talosNodeClient{
			client:  tc,
			upgrade: upgradeClients{images: tc.ImageClient, lifecycle: tc.LifecycleClient, machine: tc.MachineClient},
		}, nil
	}
}

func (t *talosNodeClient) Version(ctx context.Context) (string, error) {
	resp, err := t.client.Version(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query Talos version: %w", err)
	}

	if len(resp.GetMessages()) == 0 {
		return "", fmt.Errorf("Talos version response contained no messages")
	}

	return resp.GetMessages()[0].GetVersion().GetTag(), nil
}

func (t *talosNodeClient) ApplyConfig(ctx context.Context, data []byte) error {
	// AUTO lets Talos work out whether the change can be applied to the running system or
	// needs a reboot. Deciding that here would mean reimplementing Talos' own knowledge of
	// which config fields are hot-reloadable.
	_, err := t.client.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: data,
		Mode: machineapi.ApplyConfigurationRequest_AUTO,
	})
	if err != nil {
		return fmt.Errorf("failed to apply machine configuration: %w", err)
	}

	return nil
}

func (t *talosNodeClient) Upgrade(ctx context.Context, image string) error {
	return lifecycleUpgrade(ctx, t.upgrade, image)
}

func (t *talosNodeClient) LegacyUpgrade(ctx context.Context, image string) error {
	// Nodes before Talos 1.13 serve only the machine service upgrade, which Talos 1.14
	// deprecates; it is kept for them alone. The node performs the whole sequence itself.
	_, err := t.client.Upgrade(ctx, image, false, false) //nolint:staticcheck // pre-1.13 nodes have nothing else
	if err != nil {
		return fmt.Errorf("failed to upgrade Talos to %q: %w", image, err)
	}

	return nil
}

// apiServerPodPrefix starts the ID of the StaticPodStatus Talos keeps for the kube-apiserver
// pod, which it names "<namespace>/<pod name>"; the kubelet suffixes static pod names with the
// node name.
const apiServerPodPrefix = "kube-system/" + apiServerContainer + "-"

func (t *talosNodeClient) APIServer(ctx context.Context) (APIServerState, error) {
	var observed APIServerState

	pod, err := safe.StateGetByID[*k8s.StaticPod](ctx, t.client.COSI, k8s.APIServerID)
	if err != nil {
		if state.IsNotFoundError(err) {
			return observed, nil
		}

		return observed, fmt.Errorf("failed to read the kube-apiserver static pod: %w", err)
	}

	observed.rendered = pod.Metadata().Version().String()

	statuses, err := safe.StateListAll[*k8s.StaticPodStatus](ctx, t.client.COSI)
	if err != nil {
		return observed, fmt.Errorf("failed to list the static pod statuses: %w", err)
	}

	status, found := statuses.Find(func(s *k8s.StaticPodStatus) bool {
		return strings.HasPrefix(s.Metadata().ID(), apiServerPodPrefix)
	})
	if !found {
		return observed, nil
	}

	observed.started, observed.ready, err = apiServerPodState(status.TypedSpec().PodStatus)
	if err != nil {
		return APIServerState{}, err
	}

	return observed, nil
}

func (t *talosNodeClient) Close() error {
	return t.client.Close()
}
