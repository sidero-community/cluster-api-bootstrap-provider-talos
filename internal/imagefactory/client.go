// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package imagefactory talks to the Talos Image Factory: version listing,
// official extension listing and schematic registration.
package imagefactory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultURL is the public Image Factory.
const DefaultURL = "https://factory.talos.dev"

const maxBody = 1 << 20

// API is what the reconciler needs from the Factory. Client and Cached implement it;
// tests supply fakes.
type API interface {
	Versions(ctx context.Context) ([]string, error)
	OfficialExtensions(ctx context.Context, version string) ([]string, error)
	CreateSchematic(ctx context.Context, s Schematic) (string, error)
	InstallerImage(id, version string) string
}

// Schematic is the Factory schematic document; field names follow the Factory's own type.
type Schematic struct {
	Overlay       *Overlay      `yaml:"overlay,omitempty"`
	Customization Customization `yaml:"customization"`
}

// Overlay is the single-board-computer overlay block.
type Overlay struct {
	Image string `yaml:"image"`
	Name  string `yaml:"name"`
}

// Customization is the schematic customization block.
type Customization struct {
	SystemExtensions SystemExtensions `yaml:"systemExtensions,omitempty"`
	ExtraKernelArgs  []string         `yaml:"extraKernelArgs,omitempty"`
	Bootloader       string           `yaml:"bootloader,omitempty"`
}

// SystemExtensions lists official extensions.
type SystemExtensions struct {
	OfficialExtensions []string `yaml:"officialExtensions,omitempty"`
}

// Marshal renders the schematic as YAML for POST /schematics.
func (s Schematic) Marshal() ([]byte, error) {
	out, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshalling schematic: %w", err)
	}

	return out, nil
}

// FactoryError marks a failure talking to the Factory (transport or non-2xx), as
// opposed to a problem with what was asked of it.
type FactoryError struct {
	Err error
}

func (e *FactoryError) Error() string { return e.Err.Error() }
func (e *FactoryError) Unwrap() error { return e.Err }

// IsFactoryError reports whether err came from the Factory itself.
func IsFactoryError(err error) bool {
	var fe *FactoryError

	return errors.As(err, &fe)
}

// Client calls the Image Factory HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient validates baseURL (http or https with a host) and returns a client. A nil
// httpClient uses http.DefaultClient.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("image factory URL %q must be an http(s) URL", baseURL)
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{baseURL: strings.TrimRight(u.String(), "/"), http: httpClient}, nil
}

// Versions lists the Talos versions the Factory serves (broken ones excluded).
func (c *Client) Versions(ctx context.Context) ([]string, error) {
	raw, err := c.get(ctx, "/versions")
	if err != nil {
		return nil, err
	}

	var versions []string
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("decoding /versions: %w", err)}
	}

	return versions, nil
}

// OfficialExtensions lists the official extension names available for version, sorted.
func (c *Client) OfficialExtensions(ctx context.Context, version string) ([]string, error) {
	raw, err := c.get(ctx, "/version/"+url.PathEscape(version)+"/extensions/official")
	if err != nil {
		return nil, err
	}

	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("decoding extensions for %s: %w", version, err)}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name != "" {
			names = append(names, e.Name)
		}
	}

	sort.Strings(names)

	return names, nil
}

// CreateSchematic registers s and returns its content-addressed ID.
func (c *Client) CreateSchematic(ctx context.Context, s Schematic) (string, error) {
	body, err := s.Marshal()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/schematics", bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/yaml")
	req.Header.Set("Accept", "application/json")

	raw, err := c.do(req)
	if err != nil {
		return "", err
	}

	var reg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil || reg.ID == "" {
		return "", &FactoryError{Err: fmt.Errorf("factory returned no schematic id: %s", strings.TrimSpace(string(raw)))}
	}

	return reg.ID, nil
}

// InstallerImage is the machine.install.image reference for a schematic and version.
func (c *Client) InstallerImage(id, version string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(c.baseURL, "https://"), "http://")

	return fmt.Sprintf("%s/metal-installer/%s:%s", host, id, version)
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")

	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	res, err := c.http.Do(req) //nolint:bodyclose // closed by the deferred res.Body.Close below
	if err != nil {
		return nil, &FactoryError{Err: err}
	}
	defer res.Body.Close() //nolint:errcheck // best effort on a fully read body

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, &FactoryError{Err: fmt.Errorf("reading factory response: %w", err)}
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &FactoryError{Err: fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, res.Status, strings.TrimSpace(string(raw)))}
	}

	return raw, nil
}
