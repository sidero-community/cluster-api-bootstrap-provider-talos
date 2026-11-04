// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// apiServerSettleWindow is how long after a configuration was applied the hook keeps waiting
	// for Talos to re-render the kube-apiserver static pod before concluding that the change did
	// not touch it. Talos reacts within seconds; the window only has to outlast that.
	apiServerSettleWindow = 15 * time.Second

	// applyRecordTTL bounds how long an apply is remembered. A record this old belongs to an
	// update that was abandoned, most likely because the Machine was deleted, since Cluster API
	// stops calling the hook for a deleting Machine. A live update that outlasts it is recorded
	// again, which only costs it one more settle window.
	applyRecordTTL = 2 * time.Hour

	// apiServerContainer is the name of the kube-apiserver container in the static pod Talos
	// renders, and the prefix of the pod name the kubelet derives from it.
	apiServerContainer = "kube-apiserver"
)

// APIServerState is what the hook observes about a control plane node's kube-apiserver: the
// static pod Talos rendered for it, and the pod the kubelet runs from it.
type APIServerState struct {
	// rendered is the version of the kube-apiserver static pod resource, which Talos bumps
	// whenever it re-renders the pod for a configuration change. Empty when the resource does
	// not exist.
	rendered string

	// started is when the kube-apiserver container now running was started; zero while none
	// runs.
	started time.Time

	// ready reports whether the pod has the Ready condition.
	ready bool
}

// exists reports whether Talos has rendered a kube-apiserver static pod at all.
func (s APIServerState) exists() bool {
	return s.rendered != ""
}

// apiServerSettled reports whether a control plane node has absorbed the configuration applied
// at appliedAt, given the kube-apiserver observed just before the apply and the one observed
// now, at time at. When it has not, the reason says what is being waited for.
//
// Talos restarts kube-apiserver itself when the configuration changes what it runs with: the
// static pod is re-rendered and the kubelet replaces the pod on its next poll of the static pod
// source, up to 20 seconds later. Reporting the update complete before that would let the owner
// move on to the next control plane machine while this one's API server is still down or, with
// a configuration kube-apiserver cannot start on, roll the breakage across the whole control
// plane.
//
// Only the node's own observations are compared with each other: the static pod version is a
// counter and the container start times come from the same kubelet, so neither the node's clock
// nor this controller's enters the decision. The settle window is the one exception, and it
// is measured on this controller's clock alone.
func apiServerSettled(before, now APIServerState, appliedAt, at time.Time) (bool, string) {
	switch {
	case now.exists() && now.rendered != before.rendered:
		// Re-rendered for the new configuration: the pod has to have been replaced since.
		if !now.started.After(before.started) {
			return false, "waiting for the kubelet to restart kube-apiserver on the new configuration"
		}

		if !now.ready {
			return false, "waiting for kube-apiserver to become ready on the new configuration"
		}

		return true, ""
	case at.Before(appliedAt.Add(apiServerSettleWindow)):
		return false, "waiting for Talos to settle the kube-apiserver configuration"
	case !now.exists():
		return false, "waiting for the kube-apiserver static pod to be rendered"
	case !now.ready:
		return false, "waiting for kube-apiserver to become ready"
	default:
		// The change did not touch kube-apiserver, and it is ready.
		return true, ""
	}
}

// apiServerPodState reads the kubelet's status of the kube-apiserver pod, as Talos records it
// on its StaticPodStatus resource: when the running kube-apiserver container started, and
// whether the pod is Ready.
func apiServerPodState(podStatus map[string]any) (started time.Time, ready bool, err error) {
	encoded, err := json.Marshal(podStatus)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to encode the kube-apiserver pod status: %w", err)
	}

	var status corev1.PodStatus

	if err := json.Unmarshal(encoded, &status); err != nil {
		return time.Time{}, false, fmt.Errorf("failed to decode the kube-apiserver pod status: %w", err)
	}

	for _, condition := range status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue

			break
		}
	}

	for _, container := range status.ContainerStatuses {
		if container.Name == apiServerContainer && container.State.Running != nil {
			started = container.State.Running.StartedAt.Time

			break
		}
	}

	return started, ready, nil
}

// applyRecord is what the hook remembers about a configuration it applied to a control plane
// machine: when, and what the kube-apiserver looked like just before, so that later calls can
// tell whether the node has absorbed it.
type applyRecord struct {
	hash      string
	appliedAt time.Time
	before    APIServerState
}

// applyTracker keeps one applyRecord per machine. A controller restart loses it; the next hook
// call then records the apply again and waits one more settle window, which is safe.
type applyTracker struct {
	mu      sync.Mutex
	applies map[types.NamespacedName]applyRecord
	now     func() time.Time
}

func newApplyTracker() *applyTracker {
	return &applyTracker{applies: map[types.NamespacedName]applyRecord{}, now: time.Now}
}

// get returns the record for key, if it is for the configuration hash.
func (t *applyTracker) get(key types.NamespacedName, hash string) (applyRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	record, ok := t.applies[key]
	if !ok || record.hash != hash {
		return applyRecord{}, false
	}

	return record, true
}

// record remembers that the configuration hash was applied to key just now, with the
// kube-apiserver as observed before the apply; a record for another configuration is replaced.
// Records older than applyRecordTTL are dropped on the way.
func (t *applyTracker) record(key types.NamespacedName, hash string, before APIServerState) applyRecord {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()

	for k, r := range t.applies {
		if now.Sub(r.appliedAt) > applyRecordTTL {
			delete(t.applies, k)
		}
	}

	record := applyRecord{hash: hash, appliedAt: now, before: before}
	t.applies[key] = record

	return record
}

// forget drops the record for key.
func (t *applyTracker) forget(key types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.applies, key)
}

// configHash identifies a rendered machine configuration.
func configHash(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
