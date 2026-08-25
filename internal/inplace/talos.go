// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
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

	// Upgrade swaps the installer image, rebooting into the new version. Talos cordons and
	// drains the node itself as part of the sequence.
	Upgrade(ctx context.Context, image string) error

	// Close releases the underlying connection.
	Close() error
}

// NodeClientFactory opens a NodeClient for the given machine.
type NodeClientFactory func(ctx context.Context, clusterKey types.NamespacedName, endpoints []string) (NodeClient, error)

// talosNodeClient is the gRPC-backed NodeClient.
type talosNodeClient struct {
	client *talosclient.Client
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

		return &talosNodeClient{client: tc}, nil
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
	_, err := t.client.Upgrade(ctx, image, false, false)
	if err != nil {
		return fmt.Errorf("failed to upgrade Talos to %q: %w", image, err)
	}

	return nil
}

func (t *talosNodeClient) Close() error {
	return t.client.Close()
}
