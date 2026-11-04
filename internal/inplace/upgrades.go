// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"k8s.io/apimachinery/pkg/types"
)

// upgradeTimeout bounds one lifecycle upgrade sequence, and how long a finished sequence is
// remembered while the node comes back on the new version.
const upgradeTimeout = 30 * time.Minute

// lifecycleUpgradeMinimum is the first Talos that serves the lifecycle service.
var lifecycleUpgradeMinimum = semver.MustParse("1.13.0-alpha.2")

// lifecycleUpgradeSupported reports whether a node running the given version tag can be
// upgraded through the lifecycle service. Anything unparsable is treated as too old.
func lifecycleUpgradeSupported(runningVersion string) bool {
	version, err := semver.NewVersion(strings.TrimPrefix(runningVersion, "v"))
	if err != nil {
		return false
	}

	return !version.LessThan(lifecycleUpgradeMinimum)
}

// upgrade is one lifecycle upgrade sequence, in flight or finished, for a machine.
type upgrade struct {
	image   string
	started time.Time
	done    bool
	err     error
	cancel  context.CancelFunc
}

// upgradeTracker follows the lifecycle upgrades the hook has started, so that each hook call
// can report on a sequence instead of waiting for it. A controller restart loses it; the next
// hook call then sees the old version and starts the sequence over, which is safe because pull
// and install are idempotent.
type upgradeTracker struct {
	mu       sync.Mutex
	upgrades map[types.NamespacedName]*upgrade
	now      func() time.Time
}

func newUpgradeTracker() *upgradeTracker {
	return &upgradeTracker{upgrades: map[types.NamespacedName]*upgrade{}, now: time.Now}
}

// start records a sequence for key and runs it in the background, under a context that
// upgradeTimeout bounds and cancel ends; its outcome is kept for the next look. A sequence
// already recorded for key is left alone.
func (t *upgradeTracker) start(key types.NamespacedName, image string, run func(ctx context.Context) error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.upgrades[key]; exists {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), upgradeTimeout)

	entry := &upgrade{image: image, started: t.now(), cancel: cancel}
	t.upgrades[key] = entry

	go func() {
		err := run(ctx)

		cancel()

		t.mu.Lock()
		defer t.mu.Unlock()

		entry.done = true
		entry.err = err
	}()
}

// cancel ends the sequence recorded for key, in flight or not, and forgets it. It reports
// whether there was one.
func (t *upgradeTracker) cancel(key types.NamespacedName) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.upgrades[key]
	if !ok {
		return false
	}

	entry.cancel()
	delete(t.upgrades, key)

	return true
}

// get returns a snapshot of the sequence recorded for key.
func (t *upgradeTracker) get(key types.NamespacedName) (upgrade, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.upgrades[key]
	if !ok {
		return upgrade{}, false
	}

	return *entry, true
}

// forget drops the sequence recorded for key, releasing what it holds.
func (t *upgradeTracker) forget(key types.NamespacedName) {
	t.cancel(key)
}
