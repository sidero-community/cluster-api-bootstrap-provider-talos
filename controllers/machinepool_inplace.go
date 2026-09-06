// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/labels/format"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/inplace"
)

// poolConvergenceRequeue is how long to wait before revisiting pool members that could not be
// updated yet, e.g. because their infrastructure has not reported an address.
//
// Nothing enqueues the TalosConfig when a pool member gains an address — pool Machines carry no
// bootstrap.configRef, so the Machine watch does not map them — so an explicit requeue is what
// makes the pool converge.
const poolConvergenceRequeue = 30 * time.Second

// reconcileMachinePool brings a ready MachinePool-owned TalosConfig, and the pool's running
// members, to the configuration its current spec renders to.
//
// This is CABPT-owned behaviour, not the Cluster API in-place update flow: Cluster API has no
// hook shapes for MachinePools, pool Machines carry no bootstrap configRef for its executor to
// act on, and its machinepool controller hardcodes MachineUpToDate. Without this, an admitted
// spec change would reach neither the pool's secret nor its nodes.
func (r *TalosConfigReconciler) reconcileMachinePool(ctx context.Context, log logr.Logger, scope *TalosConfigScope) (ctrl.Result, error) {
	config := scope.Config

	secret, err := r.poolBootstrapData(ctx, log, scope)
	if err != nil {
		markDataSecretGenerationFailed(config, err)
		markPoolUnreadable(config, "Cannot render the configuration the pool should be running", err)

		return ctrl.Result{}, err
	}

	data := secret.Data["value"]
	if len(data) == 0 {
		err := fmt.Errorf("bootstrap data secret %s/%s has no value", secret.Namespace, secret.Name)
		markPoolUnreadable(config, "Cannot read the configuration the pool should be running", err)

		return ctrl.Result{}, err
	}

	// The hash on the secret, rather than the one just computed, is what a member is recorded as
	// running: it is stamped on the very bytes being applied. An unstamped secret would compare
	// equal to every un-annotated Machine and quietly skip the whole pool, so refuse it.
	appliedHash := secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation]
	if appliedHash == "" {
		err := fmt.Errorf("bootstrap data secret %s/%s carries no configuration hash", secret.Namespace, secret.Name)
		markPoolUnreadable(config, "Cannot tell which members are up to date", err)

		return ctrl.Result{}, err
	}

	machines, err := r.machinePoolMachines(ctx, scope)
	if err != nil {
		markPoolUnreadable(config, "Cannot list the pool's machines", err)

		return ctrl.Result{}, err
	}

	if len(machines) == 0 {
		setMachinePoolInPlaceCondition(config, metav1.ConditionUnknown,
			bootstrapv1beta1.MachinePoolInPlaceUpdateMachinesUnavailableReason,
			"Cluster API created no Machines for this MachinePool, so its members cannot be "+
				"reached; the rendered configuration applies to instances created from now on")

		return ctrl.Result{}, nil
	}

	converged, err := r.applyToPoolMachines(ctx, log, scope, machines, data, appliedHash)
	if err != nil {
		setMachinePoolInPlaceCondition(config, metav1.ConditionFalse,
			bootstrapv1beta1.MachinePoolInPlaceUpdateFailedReason,
			fmt.Sprintf("%d of %d MachinePool machines updated: %s", converged, len(machines), err))

		return ctrl.Result{}, err
	}

	if converged < len(machines) {
		setMachinePoolInPlaceCondition(config, metav1.ConditionFalse,
			bootstrapv1beta1.MachinePoolInPlaceUpdateInProgressReason,
			fmt.Sprintf("%d of %d MachinePool machines updated", converged, len(machines)))

		return ctrl.Result{RequeueAfter: poolConvergenceRequeue}, nil
	}

	setMachinePoolInPlaceCondition(config, metav1.ConditionTrue,
		bootstrapv1beta1.MachinePoolInPlaceUpdateUpToDateReason,
		fmt.Sprintf("%d of %d MachinePool machines updated", converged, len(machines)))

	return ctrl.Result{}, nil
}

func setMachinePoolInPlaceCondition(config *bootstrapv1beta1.TalosConfig, status metav1.ConditionStatus, reason, message string) {
	conditions.Set(config, metav1.Condition{
		Type:    bootstrapv1beta1.MachinePoolInPlaceUpdateCondition,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// markPoolUnreadable records that the pool could not be assessed at all.
//
// Every one of these paths returns before a single member is looked at, so the previous value of
// the condition says nothing about the present. A persistent failure that left an earlier True
// standing would report a pool as up to date on the strength of a reading that was never taken.
func markPoolUnreadable(config *bootstrapv1beta1.TalosConfig, what string, err error) {
	setMachinePoolInPlaceCondition(config, metav1.ConditionUnknown,
		bootstrapv1beta1.MachinePoolInPlaceUpdateInternalErrorReason,
		fmt.Sprintf("%s: %s", what, err))
}

// markMachinePoolInPlaceDisabled downgrades a condition left behind by the update loop after
// --enable-machine-pool-in-place-updates has been turned off.
//
// Only an existing condition is touched: a pool that has never been updated in place should not
// grow a condition just because the flag is off.
func markMachinePoolInPlaceDisabled(config *bootstrapv1beta1.TalosConfig) {
	existing := meta.FindStatusCondition(config.Status.Conditions, bootstrapv1beta1.MachinePoolInPlaceUpdateCondition)
	if existing == nil || existing.Reason == bootstrapv1beta1.MachinePoolInPlaceUpdateDisabledReason {
		return
	}

	setMachinePoolInPlaceCondition(config, metav1.ConditionUnknown,
		bootstrapv1beta1.MachinePoolInPlaceUpdateDisabledReason,
		"MachinePool in-place updates are disabled; the pool's members are no longer being reconciled "+
			"against the rendered configuration")
}

// poolBootstrapData returns the pool's bootstrap data secret, re-rendering it first when the
// hash it carries no longer matches the configuration the current spec renders to.
//
// The secret keeps its name across a re-render, so the MachinePool's bootstrap reference and
// every instance created from it stay valid.
func (r *TalosConfigReconciler) poolBootstrapData(ctx context.Context, log logr.Logger, scope *TalosConfigScope) (*corev1.Secret, error) {
	// The installer image an infrastructure provider resolves is per-InfraMachine, and a pool has
	// no single one; installerImageFor also only knows how to read a Machine owner, so it resolves
	// to nothing here. The renderer therefore injects no image for a pool, and this hash has to be
	// computed the same way or it would never agree with what writeBootstrapData stamped.
	desiredHash, err := renderedConfigHash(scope, "")
	if err != nil {
		return nil, err
	}

	dataSecretName := scope.ConfigOwner.GetName() + "-bootstrap-data"

	secret, err := r.fetchSecret(ctx, scope.Config, dataSecretName)
	switch {
	case err == nil && secret.Annotations[bootstrapv1beta1.InPlaceConfigHashAnnotation] == desiredHash:
		return secret, nil
	case err != nil && !k8serrors.IsNotFound(err):
		return nil, err
	}

	log.Info("re-rendering machine pool bootstrap data", "pool", scope.ConfigOwner.GetName())

	if err := r.reconcileGenerate(ctx, scope); err != nil {
		return nil, err
	}

	return r.fetchSecret(ctx, scope.Config, scope.Config.Status.DataSecretName)
}

// machinePoolMachines returns the Machines Cluster API maintains for the pool's instances, in a
// stable order.
//
// The selector is the one the Cluster API machinepool controller itself uses to find them, see
// getMachinesForMachinePool and reconcileMachines in
// internal/controllers/machinepool/machinepool_controller{,_phases}.go: the pool name (formatted
// as a label value) and the cluster name. These Machines exist only when the infrastructure
// provider publishes status.infrastructureMachineKind on its InfraMachinePool.
func (r *TalosConfigReconciler) machinePoolMachines(ctx context.Context, scope *TalosConfigScope) ([]capiv1.Machine, error) {
	poolName := scope.ConfigOwner.GetName()

	machineList := &capiv1.MachineList{}
	if err := r.Client.List(ctx, machineList,
		client.InNamespace(scope.ConfigOwner.GetNamespace()),
		client.MatchingLabels{
			capiv1.MachinePoolNameLabel: format.MustFormatValue(poolName),
			capiv1.ClusterNameLabel:     scope.Cluster.Name,
		},
	); err != nil {
		return nil, fmt.Errorf("failed listing machines for machine pool %s: %w", poolName, err)
	}

	machines := machineList.Items

	// A List has no guaranteed order, and the pool is walked one node at a time; without a stable
	// order a failure part way through would restart from an arbitrary point.
	slices.SortFunc(machines, func(a, b capiv1.Machine) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return machines, nil
}

// applyToPoolMachines pushes the rendered configuration to each pool member that is not already
// running it, one member at a time, and returns how many are converged.
//
// It stops at the first failure. Cluster API is not driving this update, so nothing else would
// notice a bad configuration; pushing it to the rest of the pool before the first failure is
// surfaced would take the whole pool down.
func (r *TalosConfigReconciler) applyToPoolMachines(
	ctx context.Context,
	log logr.Logger,
	scope *TalosConfigScope,
	machines []capiv1.Machine,
	data []byte,
	appliedHash string,
) (int, error) {
	clusterKey := types.NamespacedName{Namespace: scope.Cluster.Namespace, Name: scope.Cluster.Name}

	converged := 0

	for i := range machines {
		machine := &machines[i]

		if machine.Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation] == appliedHash {
			converged++

			continue
		}

		endpoints := inplace.MachineAddresses(machine)
		if len(endpoints) == 0 {
			// The instance exists but its infrastructure has not reported an address yet. It is
			// not a failure: an instance that boots from here on reads the re-rendered secret, and
			// one already running is picked up on a later pass.
			log.Info("machine pool member has no addresses yet", "machine", machine.Name)

			continue
		}

		log.Info("applying machine configuration to machine pool member", "machine", machine.Name)

		if err := r.applyToPoolMachine(ctx, clusterKey, machine, endpoints, data, appliedHash); err != nil {
			return converged, fmt.Errorf("failed updating machine pool member %s: %w", machine.Name, err)
		}

		converged++
	}

	return converged, nil
}

// applyToPoolMachine applies the configuration to a single pool member and records that it is
// running it.
//
// The hash is recorded only after the node has accepted the configuration, so a member that
// failed is retried rather than silently skipped on the next pass.
func (r *TalosConfigReconciler) applyToPoolMachine(
	ctx context.Context,
	clusterKey types.NamespacedName,
	machine *capiv1.Machine,
	endpoints []string,
	data []byte,
	appliedHash string,
) error {
	node, err := r.nodeClientFactory()(ctx, clusterKey, endpoints)
	if err != nil {
		return fmt.Errorf("failed connecting to the Talos API: %w", err)
	}

	defer node.Close() //nolint:errcheck // best effort on a short-lived connection

	// AUTO lets Talos decide whether the change can be applied to the running system or needs a
	// reboot. There is deliberately no Upgrade call here: a pool member is unnamed cattle that
	// Cluster API does not coordinate, so rebooting one to change its Talos version is the
	// infrastructure provider's business, not this controller's.
	if err := node.ApplyConfig(ctx, data); err != nil {
		return err
	}

	patched := machine.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = map[string]string{}
	}

	patched.Annotations[bootstrapv1beta1.AppliedConfigHashAnnotation] = appliedHash

	if err := r.Client.Patch(ctx, patched, client.MergeFrom(machine)); err != nil {
		return fmt.Errorf("failed recording the applied configuration hash: %w", err)
	}

	return nil
}

// nodeClientFactory returns the factory used to reach pool members, defaulting to a client
// built from the cluster's talosconfig secret.
func (r *TalosConfigReconciler) nodeClientFactory() inplace.NodeClientFactory {
	if r.NodeClientFactory != nil {
		return r.NodeClientFactory
	}

	return inplace.NewNodeClient(r.Client)
}
