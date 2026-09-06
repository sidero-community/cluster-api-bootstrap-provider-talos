// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

// Conditions and condition Reasons for the TalosConfig object

const (
	// TalosConfigReadyCondition condition is true when all ClientConfigAvailable and DataSecretAvailable conditions are true.
	TalosConfigReadyCondition = capiv1.ReadyCondition

	// TalosConfigReadyReason surfaces when TalosConfig is ready.
	TalosConfigReadyReason = capiv1.ReadyReason

	// TalosConfigNotReadyReason surfaces when TalosConfig is not ready.
	TalosConfigNotReadyReason = capiv1.NotReadyReason

	// TalosConfigReadyUnknownReason surfaces when TalosConfig readiness is unknown.
	TalosConfigReadyUnknownReason = capiv1.ReadyUnknownReason
)

const (
	// ClientConfigAvailableCondition documents the status of the client config generation process.
	ClientConfigAvailableCondition = "ClientConfigAvailable"

	// ClientConfigAvailableReason surfaces when generated Talos client config is available.
	ClientConfigAvailableReason = capiv1.AvailableReason

	// ClientConfigAvailableInternalErrorReason surfaces unexpected failures when reading or generating
	// Talos client config.
	ClientConfigAvailableInternalErrorReason = capiv1.InternalErrorReason
)

const (
	// DataSecretAvailableCondition documents the status of the bootstrap secret generation process.
	//
	// NOTE: When the DataSecret generation starts the process completes immediately and within the
	// same reconciliation, so the user will always see a transition from Wait to Generated without having
	// evidence that BootstrapSecret generation is started/in progress.
	DataSecretAvailableCondition = "DataSecretAvailable"

	// DataSecretAvailableReason surfaces when Talos bootstrap secret is available.
	DataSecretAvailableReason = capiv1.AvailableReason

	// DataSecretNotAvailableCondition surfaces when Talos bootstrap secret is not available.
	DataSecretNotAvailableReason = capiv1.NotAvailableReason

	// DataSecretNotAvailableInternalErrorReason surfaces unexpected failures when generating Talos bootstrap secret.
	DataSecretNotAvailableInternalErrorReason = capiv1.InternalErrorReason
)

const (
	// MachinePoolInPlaceUpdateCondition documents whether the members of a MachinePool are
	// running the machine configuration currently rendered for the pool.
	//
	// Cluster API offers no in-place update flow for MachinePools — its machinepool controller
	// hardcodes MachineUpToDate — so this condition, and not any Machine status, is where the
	// progress of a pool-wide configuration change is reported.
	MachinePoolInPlaceUpdateCondition = "MachinePoolInPlaceUpdate"

	// MachinePoolInPlaceUpdateUpToDateReason surfaces when every member of the pool is running
	// the currently rendered machine configuration.
	MachinePoolInPlaceUpdateUpToDateReason = "UpToDate"

	// MachinePoolInPlaceUpdateInProgressReason surfaces when some members of the pool have not
	// been brought to the currently rendered machine configuration yet, e.g. because their
	// infrastructure has not reported an address.
	MachinePoolInPlaceUpdateInProgressReason = "InProgress"

	// MachinePoolInPlaceUpdateFailedReason surfaces when applying the machine configuration to a
	// pool member failed. The pool is walked one node at a time and stops at the first failure.
	MachinePoolInPlaceUpdateFailedReason = "ApplyFailed"

	// MachinePoolInPlaceUpdateInternalErrorReason surfaces when the pool could not be assessed at
	// all, e.g. its Machines could not be listed or its bootstrap data could not be rendered.
	//
	// The condition goes to Unknown rather than staying on its last value: reporting a pool as up
	// to date on the strength of a reading that could not be taken is worse than reporting
	// nothing.
	MachinePoolInPlaceUpdateInternalErrorReason = capiv1.InternalErrorReason

	// MachinePoolInPlaceUpdateDisabledReason surfaces when a pool that was being updated in place
	// no longer is, because --enable-machine-pool-in-place-updates was turned off. Without it the
	// last value the update loop wrote would stand forever.
	MachinePoolInPlaceUpdateDisabledReason = "Disabled"

	// MachinePoolInPlaceUpdateMachinesUnavailableReason surfaces when the pool has no Machines to
	// update.
	//
	// Cluster API only creates Machines for pool members when the infrastructure provider
	// publishes status.infrastructureMachineKind on its InfraMachinePool. Without them there is
	// no supported way to find the pool's nodes, so the rendered configuration reaches new
	// instances only.
	MachinePoolInPlaceUpdateMachinesUnavailableReason = "PoolMachinesUnavailable"
)
