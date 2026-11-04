// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The decision only compares the node's own observations with each other: a re-rendered static
// pod has to be followed by a container that started later, and a static pod left alone only has
// to be ready once Talos had time to react.
func TestAPIServerSettled(t *testing.T) {
	t.Parallel()

	applied := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	earlier := applied.Add(-time.Hour)
	later := applied.Add(10 * time.Second)

	before := APIServerState{rendered: "3", started: earlier, ready: true}

	for name, tc := range map[string]struct {
		before  APIServerState
		now     APIServerState
		at      time.Time
		settled bool
		reason  string
	}{
		"re-rendered, pod not replaced yet": {
			before: before,
			now:    APIServerState{rendered: "4", started: earlier, ready: true},
			at:     applied.Add(time.Second),
			reason: "restart kube-apiserver",
		},
		"re-rendered, pod replaced, not ready": {
			before: before,
			now:    APIServerState{rendered: "4", started: later, ready: false},
			at:     applied.Add(time.Second),
			reason: "become ready on the new configuration",
		},
		"re-rendered, pod replaced, ready": {
			before:  before,
			now:     APIServerState{rendered: "4", started: later, ready: true},
			at:      applied.Add(time.Second),
			settled: true,
		},
		"re-rendered, container not running at all": {
			before: before,
			now:    APIServerState{rendered: "4", ready: false},
			at:     applied.Add(time.Minute),
			reason: "restart kube-apiserver",
		},
		"not yet re-rendered inside the settle window": {
			before: before,
			now:    before,
			at:     applied.Add(apiServerSettleWindow - time.Second),
			reason: "settle",
		},
		"untouched after the settle window, ready": {
			before:  before,
			now:     before,
			at:      applied.Add(apiServerSettleWindow),
			settled: true,
		},
		"untouched after the settle window, not ready": {
			before: before,
			now:    APIServerState{rendered: "3", started: earlier, ready: false},
			at:     applied.Add(time.Minute),
			reason: "become ready",
		},
		"static pod never rendered": {
			before: APIServerState{},
			now:    APIServerState{},
			at:     applied.Add(time.Minute),
			reason: "static pod to be rendered",
		},
		"static pod rendered for the first time": {
			before:  APIServerState{},
			now:     APIServerState{rendered: "1", started: later, ready: true},
			at:      applied.Add(time.Second),
			settled: true,
		},
		// After a reboot the resource versions start over and may coincide with the ones
		// observed before; the node has to be ready either way.
		"rebooted onto the same version number, not ready": {
			before: before,
			now:    APIServerState{rendered: "3", started: later, ready: false},
			at:     applied.Add(10 * time.Minute),
			reason: "become ready",
		},
		"rebooted onto the same version number, ready": {
			before:  before,
			now:     APIServerState{rendered: "3", started: later, ready: true},
			at:      applied.Add(10 * time.Minute),
			settled: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			settled, reason := apiServerSettled(tc.before, tc.now, applied, tc.at)

			assert.Equal(t, tc.settled, settled, reason)

			if tc.reason == "" {
				assert.Empty(t, reason)
			} else {
				assert.Contains(t, reason, tc.reason)
			}
		})
	}
}

// Talos records the kubelet's PodStatus on the resource as a generic map, exactly as JSON
// would encode it.
func TestAPIServerPodState(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	encode := func(t *testing.T, status corev1.PodStatus) map[string]any {
		t.Helper()

		data, err := json.Marshal(status)
		require.NoError(t, err)

		var out map[string]any

		require.NoError(t, json.Unmarshal(data, &out))

		return out
	}

	started, ready, err := apiServerPodState(encode(t, corev1.PodStatus{
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  apiServerContainer,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(startedAt)}},
		}},
	}))
	require.NoError(t, err)
	assert.True(t, ready)
	assert.True(t, started.Equal(startedAt), started)

	started, ready, err = apiServerPodState(encode(t, corev1.PodStatus{
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  apiServerContainer,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}},
	}))
	require.NoError(t, err)
	assert.False(t, ready)
	assert.True(t, started.IsZero(), "a container that is not running has not started")

	started, ready, err = apiServerPodState(nil)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.True(t, started.IsZero())
}

func TestApplyTracker(t *testing.T) {
	t.Parallel()

	key := types.NamespacedName{Namespace: "default", Name: "machine-1"}
	other := types.NamespacedName{Namespace: "default", Name: "machine-2"}

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	tracker := newApplyTracker()
	tracker.now = func() time.Time { return now }

	_, ok := tracker.get(key, "a")
	assert.False(t, ok)

	before := APIServerState{rendered: "3", ready: true}

	record := tracker.record(key, "a", before)
	assert.Equal(t, applyRecord{hash: "a", appliedAt: now, before: before}, record)

	got, ok := tracker.get(key, "a")
	require.True(t, ok)
	assert.Equal(t, record, got)

	_, ok = tracker.get(key, "b")
	assert.False(t, ok, "a record is for one configuration only")

	// A stale record is dropped when another machine's apply is recorded.
	now = now.Add(applyRecordTTL + time.Second)
	tracker.record(other, "c", APIServerState{})

	_, ok = tracker.get(key, "a")
	assert.False(t, ok, "records older than the TTL are pruned")

	tracker.forget(other)
	_, ok = tracker.get(other, "c")
	assert.False(t, ok)
}

func TestConfigHashIdentifiesTheConfiguration(t *testing.T) {
	t.Parallel()

	assert.Equal(t, configHash([]byte("a")), configHash([]byte("a")))
	assert.NotEqual(t, configHash([]byte("a")), configHash([]byte("b")))
}
