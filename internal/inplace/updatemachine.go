// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/util"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
)

const (
	// waitForConfigSeconds is how long to wait before re-checking whether CABPT has
	// regenerated the bootstrap data for the desired spec.
	waitForConfigSeconds = 10

	// waitForRebootSeconds is how long to wait for a node to come back after a change that
	// Talos decided needed a reboot, or after an upgrade.
	waitForRebootSeconds = 30

	// deletionPollInterval is how often a running Talos upgrade checks whether its Machine is
	// being deleted.
	deletionPollInterval = 15 * time.Second
)

// DoUpdateMachine performs the in-place update of a single machine.
//
// Cluster API calls this repeatedly until it reports completion, so every step observes the
// node's actual state and acts only on a difference. Returning Success with a non-zero
// RetryAfterSeconds means "still working"; Success with zero means done.
//
// On a control plane machine, done also means that the node's kube-apiserver has absorbed the
// configuration: Talos restarts it on its own when the configuration changes what it runs with,
// and the owner must not move on to the next machine before it is back.
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

	machineKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Name}
	controlPlane := util.IsControlPlaneMachine(&machine)
	hash := configHash(desiredConfig)

	// On a control plane machine the kube-apiserver is observed before the configuration goes
	// on, so that a restart Talos makes for it can be told from the pod that ran before.
	var (
		record  applyRecord
		tracked bool
		before  APIServerState
	)

	if controlPlane {
		record, tracked = h.applies.get(machineKey, hash)

		if !tracked {
			before, err = node.APIServer(ctx)
			if err != nil {
				log.Info("cannot observe kube-apiserver, will retry", "error", err.Error())

				resp.Status = runtimehooksv1.ResponseStatusSuccess
				resp.Message = "waiting for the node to report its kube-apiserver"
				resp.RetryAfterSeconds = waitForRebootSeconds

				return
			}
		}
	}

	// Apply the configuration first so that the node has the desired installer image
	// recorded before any upgrade is triggered, and so config-only changes settle without a
	// reboot wherever Talos allows it.
	if err := node.ApplyConfig(ctx, desiredConfig); err != nil {
		respondRetryFailure(&resp.CommonRetryResponse, "failed to apply machine configuration", err, log)

		return
	}

	if controlPlane && !tracked {
		record = h.applies.record(machineKey, hash, before)
	}

	if needsUpgrade(desiredImage, runningVersion) {
		h.continueUpgrade(ctx, log, resp, node, upgradeTarget{
			machine:   machineKey,
			cluster:   clusterKey,
			endpoints: endpoints,
			image:     desiredImage,
			running:   runningVersion,
		})

		return
	}

	if controlPlane {
		observed, err := node.APIServer(ctx)
		if err != nil {
			log.Info("cannot observe kube-apiserver, will retry", "error", err.Error())

			resp.Status = runtimehooksv1.ResponseStatusSuccess
			resp.Message = "waiting for the node to report its kube-apiserver"
			resp.RetryAfterSeconds = waitForRebootSeconds

			return
		}

		if settled, reason := apiServerSettled(record.before, observed, record.appliedAt, h.applies.now()); !settled {
			log.Info(reason)

			resp.Status = runtimehooksv1.ResponseStatusSuccess
			resp.Message = reason
			resp.RetryAfterSeconds = waitForConfigSeconds

			return
		}

		h.applies.forget(machineKey)
	}

	// The node runs the desired version, so any sequence recorded for it is over.
	h.upgrades.forget(machineKey)

	log.Info("in-place update applied")

	resp.Status = runtimehooksv1.ResponseStatusSuccess
	resp.RetryAfterSeconds = 0
}

// upgradeTarget is what continueUpgrade needs to know about the machine being upgraded.
type upgradeTarget struct {
	machine   types.NamespacedName
	cluster   types.NamespacedName
	endpoints []string
	image     string
	running   string
}

// continueUpgrade moves a Talos upgrade along by one hook call.
//
// Nodes before Talos 1.13 upgrade themselves through the machine service and need nothing more
// than the request. Newer nodes are driven through the lifecycle service, whose sequence takes
// minutes; it runs in the background under the tracker, and each call reports on it: started,
// still running, failed (once, then the sequence is retried from scratch) or finished and
// waiting for the node to come back on the new version.
func (h *Handler) continueUpgrade(ctx context.Context, log logr.Logger, resp *runtimehooksv1.UpdateMachineResponse, node NodeClient, target upgradeTarget) {
	if !lifecycleUpgradeSupported(target.running) {
		log.Info("upgrading Talos in place through the machine service", "image", target.image, "running", target.running)

		if err := node.LegacyUpgrade(ctx, target.image); err != nil {
			respondRetryFailure(&resp.CommonRetryResponse, "failed to upgrade Talos", err, log)

			return
		}

		respondRetry(resp, fmt.Sprintf("upgrading Talos to %s", target.image))

		return
	}

	current, tracked := h.upgrades.get(target.machine)

	switch {
	case !tracked:
		log.Info("starting a lifecycle upgrade", "image", target.image, "running", target.running)

		h.upgrades.start(target.machine, target.image, func(ctx context.Context) error {
			go h.cancelOnDeletion(ctx, target.machine)

			// The request-scoped client is closed when this hook call returns; the sequence
			// outlives it and needs a connection of its own.
			background, err := h.newNodeClient(ctx, target.cluster, target.endpoints)
			if err != nil {
				return fmt.Errorf("failed to connect for the upgrade: %w", err)
			}

			defer background.Close() //nolint:errcheck // best effort

			return background.Upgrade(ctx, target.image)
		})

		respondRetry(resp, fmt.Sprintf("upgrading Talos to %s", target.image))
	case current.err != nil:
		h.upgrades.forget(target.machine)
		respondRetryFailure(&resp.CommonRetryResponse, "failed to upgrade Talos", current.err, log)
	case current.done && time.Since(current.started) > upgradeTimeout:
		// The install finished long ago and the node never came back on the new version.
		log.Info("the node did not come back on the new version, restarting the upgrade", "image", target.image)
		h.upgrades.forget(target.machine)
		respondRetry(resp, fmt.Sprintf("restarting the upgrade to %s", target.image))
	case current.done:
		respondRetry(resp, fmt.Sprintf("waiting for the node to reboot into %s", target.image))
	default:
		respondRetry(resp, fmt.Sprintf("upgrading Talos to %s", target.image))
	}
}

// cancelOnDeletion watches the Machine while its lifecycle upgrade runs, and cancels the
// upgrade once the Machine is being deleted or is gone.
//
// Cluster API stops calling the hook for a deleting Machine, so nothing else would stop a
// sequence that is about to reboot a node on its way out: the control plane provider needs the
// node to answer while it removes the etcd member, and the infrastructure provider may already
// be wiping the disk the install writes to. This is what lets the control plane provider
// remediate a machine in the middle of its update. The poll is cheap: the Machine comes from
// the manager's cache.
func (h *Handler) cancelOnDeletion(ctx context.Context, key types.NamespacedName) {
	log := ctrl.Log.WithName("inplace").WithValues("machine", key.String())

	ticker := time.NewTicker(h.deletionPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		machine := &clusterv1.Machine{}

		err := h.client.Get(ctx, key, machine)

		switch {
		case apierrors.IsNotFound(err):
			log.Info("cancelling the Talos upgrade of a machine that is gone")
		case err != nil:
			log.Info("cannot tell whether the machine is being deleted, will retry", "error", err.Error())

			continue
		case machine.DeletionTimestamp.IsZero():
			continue
		default:
			log.Info("cancelling the Talos upgrade of a machine being deleted")
		}

		h.upgrades.cancel(key)
		h.applies.forget(key)

		return
	}
}

// respondRetry asks Cluster API to call again after the reboot wait.
func respondRetry(resp *runtimehooksv1.UpdateMachineResponse, message string) {
	resp.Status = runtimehooksv1.ResponseStatusSuccess
	resp.Message = message
	resp.RetryAfterSeconds = waitForRebootSeconds
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

// installImage reads the installer image out of a rendered Talos machine configuration, with
// the precedence Talos applies: an UnattendedInstallConfig document first, the legacy
// machine.install.image field otherwise.
func installImage(data []byte) (string, error) {
	provider, err := configloader.NewFromBytes(data)
	if err != nil {
		return "", fmt.Errorf("failed to parse machine configuration: %w", err)
	}

	if doc := provider.UnattendedInstallConfig(); doc != nil {
		return doc.InstallerImage(), nil
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

	return MachineAddresses(live), nil
}

// MachineAddresses returns the addresses usable as Talos API endpoints for a machine.
//
// It is exported because the MachinePool in-place path in the TalosConfig controller resolves
// endpoints for pool members exactly the same way; Cluster API has no in-place update flow for
// MachinePools, so that path cannot go through the hook handler.
func MachineAddresses(machine *clusterv1.Machine) []string {
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

// installerImage reads the installer image CABPT resolved for the machine's TalosConfig.
//
// It is read from the live TalosConfig rather than taken from the hook request: Cluster API
// strips status before sending, and the image lives in status.imageFactory. Without it the
// hash recomputed here could not match the one CABPT stamped, and a genuinely fresh secret
// would be mistaken for a stale one.
func (h *Handler) installerImage(ctx context.Context, machine *clusterv1.Machine) (string, error) {
	ref := machine.Spec.Bootstrap.ConfigRef
	if !ref.IsDefined() {
		return "", nil
	}

	config := &bootstrapv1beta1.TalosConfig{}

	key := types.NamespacedName{Namespace: machine.Namespace, Name: ref.Name}
	if err := h.client.Get(ctx, key, config); err != nil {
		return "", fmt.Errorf("failed to read TalosConfig %s for the installer image: %w", key, err)
	}

	if config.Status.ImageFactory == nil {
		return "", nil
	}

	return config.Status.ImageFactory.InstallerImage, nil
}
