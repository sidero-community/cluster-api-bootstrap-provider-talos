// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

var trackerKey = types.NamespacedName{Namespace: "default", Name: "machine-1"}

func TestUpgradeTrackerRunsTheSequenceOnceAndRecordsTheOutcome(t *testing.T) {
	t.Parallel()

	tracker := newUpgradeTracker()
	done := make(chan struct{})

	tracker.start(trackerKey, "img", func(context.Context) error {
		<-done

		return errors.New("install exited with code 1")
	})

	current, ok := tracker.get(trackerKey)
	require.True(t, ok)
	assert.Equal(t, "img", current.image)
	assert.False(t, current.done)
	assert.NoError(t, current.err)

	close(done)

	require.Eventually(t, func() bool {
		current, _ := tracker.get(trackerKey)

		return current.done
	}, time.Second, 10*time.Millisecond)

	current, _ = tracker.get(trackerKey)
	assert.EqualError(t, current.err, "install exited with code 1")

	tracker.forget(trackerKey)
	_, ok = tracker.get(trackerKey)
	assert.False(t, ok)
}

// A cancelled sequence sees its context end, so that a blocked install stream or reboot call
// returns, and is forgotten at once rather than reported on.
func TestUpgradeTrackerCancelEndsTheSequence(t *testing.T) {
	t.Parallel()

	tracker := newUpgradeTracker()
	ended := make(chan error, 1)

	tracker.start(trackerKey, "img", func(ctx context.Context) error {
		<-ctx.Done()
		ended <- ctx.Err()

		return ctx.Err()
	})

	assert.True(t, tracker.cancel(trackerKey))

	select {
	case err := <-ended:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("the sequence did not see the cancellation")
	}

	_, ok := tracker.get(trackerKey)
	assert.False(t, ok)
	assert.False(t, tracker.cancel(trackerKey), "nothing left to cancel")
}

func TestLifecycleUpgradeSupported(t *testing.T) {
	t.Parallel()

	assert.True(t, lifecycleUpgradeSupported("v1.14.0"))
	assert.True(t, lifecycleUpgradeSupported("v1.13.0"))
	assert.True(t, lifecycleUpgradeSupported("v1.13.0-alpha.2"))
	assert.False(t, lifecycleUpgradeSupported("v1.13.0-alpha.1"))
	assert.False(t, lifecycleUpgradeSupported("v1.12.3"))
	assert.False(t, lifecycleUpgradeSupported(""))
	assert.False(t, lifecycleUpgradeSupported("garbage"))
}
