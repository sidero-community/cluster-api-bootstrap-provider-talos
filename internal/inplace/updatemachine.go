// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/installerimage"
)

const (
	// waitForConfigSeconds is how long to wait before re-checking whether CABPT has
	// regenerated the bootstrap data for the desired spec.
	waitForConfigSeconds = 10

	// waitForRebootSeconds is how long to wait for a node to come back after a change that
	// Talos decided needed a reboot, or after an upgrade.
	waitForRebootSeconds = 30
)

// DoUpdateMachine performs the in-place update of a single machine.
//
// Cluster API calls this repeatedly until it reports completion, so every step observes the
// node's actual state and acts only on a difference. Returning Success with a non-zero
// RetryAfterSeconds means "still working"; Success with zero means done.
func (h *Handler) DoUpdateMachine(ctx context.Context, req *runtimehooksv1.UpdateMachineRequest, resp *runtimehooksv1.UpdateMachineResponse) {
	log := ctrl.LoggerFrom(ctx)

	machine := req.Desired.Machine

	desiredConfig, ready, err := h.desiredConfig(ctx, &machine, req.Desired.BootstrapConfig)
	if err != nil {
		respondRetryFailure(&resp.CommonRetryResponse, "failed to resolve desired machine configuration", err, log)

		return
	}

	if !ready {
		// The owning controller has written the new desired spec but CABPT has not yet
		// regenerated the data secret. Applying what is there now would push the pre-update
		// configuration and then report success, silently losing the change.
		log.Info("waiting for bootstrap data to be regenerated for the desired spec")

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.Message = "waiting for bootstrap configuration to be regenerated"
		resp.RetryAfterSeconds = waitForConfigSeconds

		return
	}

	endpoints, err := h.machineEndpoints(ctx, &machine)
	if err != nil {
		respondRetryFailure(&resp.CommonRetryResponse, "failed to read machine addresses", err, log)

		return
	}

	if len(endpoints) == 0 {
		log.Info("machine has no addresses yet, waiting")

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.Message = "waiting for machine addresses"
		resp.RetryAfterSeconds = waitForConfigSeconds

		return
	}

	clusterKey := types.NamespacedName{
		Namespace: machine.Namespace,
		Name:      machine.Spec.ClusterName,
	}

	node, err := h.newNodeClient(ctx, clusterKey, endpoints)
	if err != nil {
		// The node may simply be rebooting as part of this update; retry rather than fail.
		log.Info("cannot reach Talos API, will retry", "error", err.Error())

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.Message = "waiting for the Talos API to become reachable"
		resp.RetryAfterSeconds = waitForRebootSeconds

		return
	}

	defer node.Close() //nolint:errcheck // best effort on a read-mostly connection

	desiredImage, err := installImage(desiredConfig)
	if err != nil {
		respondRetryFailure(&resp.CommonRetryResponse, "failed to read desired installer image", err, log)

		return
	}

	runningVersion, err := node.Version(ctx)
	if err != nil {
		log.Info("cannot read Talos version, will retry", "error", err.Error())

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.Message = "waiting for the node to report its version"
		resp.RetryAfterSeconds = waitForRebootSeconds

		return
	}

	// Apply the configuration first so that the node has the desired installer image
	// recorded before any upgrade is triggered, and so config-only changes settle without a
	// reboot wherever Talos allows it.
	if err := node.ApplyConfig(ctx, desiredConfig); err != nil {
		respondRetryFailure(&resp.CommonRetryResponse, "failed to apply machine configuration", err, log)

		return
	}

	if needsUpgrade(desiredImage, runningVersion) {
		log.Info("upgrading Talos in place", "image", desiredImage, "running", runningVersion)

		if err := node.Upgrade(ctx, desiredImage); err != nil {
			respondRetryFailure(&resp.CommonRetryResponse, "failed to upgrade Talos", err, log)

			return
		}

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.Message = fmt.Sprintf("upgrading Talos to %s", desiredImage)
		resp.RetryAfterSeconds = waitForRebootSeconds

		return
	}

	log.Info("in-place update applied")

	resp.Status = runtimehooksv1.ResponseStatusSuccess
	resp.RetryAfterSeconds = 0
}

// desiredConfig fetches the rendered machine configuration for the desired spec.
//
// The second return value reports whether the data secret has actually been regenerated for
// the desired spec yet, established by comparing the hash CABPT stamps on the secret against
// a hash recomputed from the desired inputs in this request.
func (h *Handler) desiredConfig(ctx context.Context, machine *clusterv1.Machine, bootstrapConfig runtimeRawExtension) ([]byte, bool, error) {
	secretName := machine.Spec.Bootstrap.DataSecretName
	if secretName == nil || *secretName == "" {
		return nil, false, fmt.Errorf("machine has no bootstrap data secret")
	}

	var secret corev1.Secret

	key := types.NamespacedName{Namespace: machine.Namespace, Name: *secretName}
	if err := h.client.Get(ctx, key, &secret); err != nil {
		return nil, false, fmt.Errorf("failed to read bootstrap data secret %s: %w", key, err)
	}

	data, ok := secret.Data["value"]
	if !ok || len(data) == 0 {
		return nil, false, fmt.Errorf("bootstrap data secret %s has no value", key)
	}

	installerImage, err := h.installerImage(ctx, machine)
	if err != nil {
		return nil, false, err
	}

	expected, err := desiredConfigHash(machine, bootstrapConfig, installerImage)
	if err != nil {
		return nil, false, err
	}

	// An empty expected hash means the desired TalosConfig could not be interpreted, which
	// should not silently pass as "fresh".
	if expected == "" {
		return nil, false, fmt.Errorf("could not compute the desired configuration hash")
	}

	return data, secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation] == expected, nil
}

// desiredConfigHash recomputes, from the desired objects in the hook request, the hash CABPT
// stamps on a regenerated bootstrap data secret.
func desiredConfigHash(machine *clusterv1.Machine, bootstrapConfig runtimeRawExtension, installerImage string) (string, error) {
	obj, err := rawToUnstructured(bootstrapConfig)
	if err != nil {
		return "", err
	}

	if obj == nil {
		return "", fmt.Errorf("desired bootstrap config was not provided")
	}

	var spec bootstrapv1beta1.TalosConfigSpec

	if err := remarshalInto(obj["spec"], &spec); err != nil {
		return "", fmt.Errorf("failed to decode desired TalosConfig spec: %w", err)
	}

	return bootstrapv1beta1.InPlaceConfigHash(spec, strings.TrimPrefix(machine.Spec.Version, "v"), installerImage)
}

// installImage reads machine.install.image out of a rendered Talos machine configuration.
func installImage(data []byte) (string, error) {
	provider, err := configloader.NewFromBytes(data)
	if err != nil {
		return "", fmt.Errorf("failed to parse machine configuration: %w", err)
	}

	if provider.Machine() == nil || provider.Machine().Install() == nil {
		return "", nil
	}

	return provider.Machine().Install().Image(), nil
}

// needsUpgrade reports whether the node has to be upgraded to reach the desired installer
// image.
//
// Talos reports its running version as a tag such as "v1.13.0", and installer images are
// conventionally tagged with exactly that version, so comparing the image tag against the
// running tag avoids rebooting a node that is already on the target version. An image
// without a recognisable version tag is treated as "no upgrade needed": guessing wrong in
// the other direction would reboot the node on every reconcile.
func needsUpgrade(desiredImage, runningVersion string) bool {
	if desiredImage == "" || runningVersion == "" {
		return false
	}

	tag := imageTag(desiredImage)
	if !talosVersionTag.MatchString(tag) {
		// A floating tag such as "latest" carries no version to compare against, so every
		// reconcile after a successful upgrade would see a mismatch and reboot the node again.
		// Treating it as "no upgrade needed" is the safe direction.
		return false
	}

	return tag != runningVersion
}

// talosVersionTag matches an installer tag that names a Talos version, e.g. "v1.13.9". Tags that
// do not, "latest" most of all, cannot be compared against the running version.
var talosVersionTag = regexp.MustCompile(`^v\d+\.\d+\.\d+`)

// imageTag extracts the tag from an image reference, ignoring any digest and being careful
// not to mistake a registry port for a tag.
func imageTag(image string) string {
	if at := strings.LastIndex(image, "@"); at >= 0 {
		image = image[:at]
	}

	colon := strings.LastIndex(image, ":")
	if colon < 0 {
		return ""
	}

	// A colon that precedes a slash belongs to a registry host:port, not a tag.
	if strings.Contains(image[colon:], "/") {
		return ""
	}

	return image[colon+1:]
}

// machineEndpoints returns the addresses usable as Talos API endpoints for a machine.
//
// The Machine is re-read from the API server rather than taken from the hook request. Cluster
// API strips status when building an UpdateMachine request, treating it purely as desired state,
// so the addresses on the request object are always empty and waiting on them never completes.
func (h *Handler) machineEndpoints(ctx context.Context, machine *clusterv1.Machine) ([]string, error) {
	live := &clusterv1.Machine{}

	key := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Name}
	if err := h.client.Get(ctx, key, live); err != nil {
		return nil, fmt.Errorf("reading machine %s: %w", key, err)
	}

	return machineAddresses(live), nil
}

// machineAddresses returns the addresses usable as Talos API endpoints for a machine.
func machineAddresses(machine *clusterv1.Machine) []string {
	out := make([]string, 0, len(machine.Status.Addresses))

	for _, addr := range machine.Status.Addresses {
		switch addr.Type {
		case clusterv1.MachineInternalIP, clusterv1.MachineExternalIP:
			out = append(out, addr.Address)
		default:
		}
	}

	return out
}

// installerImage reads the installer image resolved for a machine, from the
// same source the config writer uses (internal/installerimage: the Hardware
// annotation first, the InfraMachine status as transition fallback).
//
// It is read live rather than taken from the hook request — Cluster API strips
// status before sending — and it MUST resolve identically to the writer's
// read: otherwise the hash recomputed here could never match the one CABPT
// stamped, and a genuinely fresh secret would be mistaken for a stale one.
func (h *Handler) installerImage(ctx context.Context, machine *clusterv1.Machine) (string, error) {
	return installerimage.Resolve(ctx, h.client, machine)
}
