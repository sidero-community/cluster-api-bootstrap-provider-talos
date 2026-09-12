// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package imagefactory

import (
	"context"
	"sync"
	"time"
)

// Cached wraps an API with in-memory caches: schematic IDs by body (content addressed,
// never expire), version and extension lists with a TTL and stale-on-error fallback.
type Cached struct {
	api API
	ttl time.Duration
	now func() time.Time

	mu         sync.Mutex
	schematics map[string]string
	versions   *listEntry
	extensions map[string]*listEntry
}

type listEntry struct {
	values  []string
	fetched time.Time
}

// NewCached returns a caching wrapper around api.
func NewCached(api API, ttl time.Duration) *Cached {
	return &Cached{api: api, ttl: ttl, now: time.Now, schematics: map[string]string{}, extensions: map[string]*listEntry{}}
}

// Versions implements API.
func (c *Cached) Versions(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.refresh(c.versions, func() ([]string, error) { return c.api.Versions(ctx) })
	if err != nil {
		return nil, err
	}

	c.versions = entry

	return entry.values, nil
}

// OfficialExtensions implements API.
func (c *Cached) OfficialExtensions(ctx context.Context, version string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, err := c.refresh(c.extensions[version], func() ([]string, error) { return c.api.OfficialExtensions(ctx, version) })
	if err != nil {
		return nil, err
	}

	c.extensions[version] = entry

	return entry.values, nil
}

// CreateSchematic implements API; a schematic body registered once is never re-sent.
func (c *Cached) CreateSchematic(ctx context.Context, s Schematic) (string, error) {
	body, err := s.Marshal()
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	id, ok := c.schematics[string(body)]
	c.mu.Unlock()

	if ok {
		return id, nil
	}

	id, err = c.api.CreateSchematic(ctx, s)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.schematics[string(body)] = id
	c.mu.Unlock()

	return id, nil
}

// InstallerImage implements API.
func (c *Cached) InstallerImage(id, version string) string {
	return c.api.InstallerImage(id, version)
}

// refresh returns entry when it is fresh, otherwise fetches; on a fetch error a stale
// entry is returned instead of the error.
func (c *Cached) refresh(entry *listEntry, fetch func() ([]string, error)) (*listEntry, error) {
	if entry != nil && c.now().Sub(entry.fetched) < c.ttl {
		return entry, nil
	}

	values, err := fetch()
	if err != nil {
		if entry != nil {
			return entry, nil
		}

		return nil, err
	}

	return &listEntry{values: values, fetched: c.now()}, nil
}
