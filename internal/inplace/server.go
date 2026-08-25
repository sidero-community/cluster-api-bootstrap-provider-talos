// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	runtimeserver "sigs.k8s.io/cluster-api/exp/runtime/server"
)

// runtimeRawExtension aliases the wire type used for provider-specific objects in hook
// requests, so call sites read as "some object we do not have a Go type for".
type runtimeRawExtension = runtime.RawExtension

// Handler serves the Cluster API in-place update hooks for Talos machines.
type Handler struct {
	client        client.Client
	newNodeClient NodeClientFactory
}

// NewHandler builds a Handler backed by the management cluster client c, opening Talos
// connections through factory. Passing a fake factory is how the update state machine is
// tested without hardware.
func NewHandler(c client.Client, factory NodeClientFactory) *Handler {
	return &Handler{
		client:        c,
		newNodeClient: factory,
	}
}

// Options configures the runtime extension server.
type Options struct {
	// Host is the address to listen on. Empty means all addresses.
	Host string

	// Port is the port to serve on.
	Port int

	// CertDir holds tls.crt and tls.key for the server.
	CertDir string
}

// AddToManager registers a runtime extension server serving the in-place update hooks as a
// Runnable on mgr, so it shares the manager's lifecycle and shuts down with it.
//
// Cluster API reaches these hooks through an ExtensionConfig pointing at this server; see
// config/runtime-extension for the manifests.
func AddToManager(mgr manager.Manager, handler *Handler, opts Options) error {
	catalog := runtimecatalog.New()
	if err := runtimehooksv1.AddToCatalog(catalog); err != nil {
		return fmt.Errorf("failed to build runtime hook catalog: %w", err)
	}

	srv, err := runtimeserver.New(runtimeserver.Options{
		Catalog: catalog,
		Host:    opts.Host,
		Port:    opts.Port,
		CertDir: opts.CertDir,
	})
	if err != nil {
		return fmt.Errorf("failed to create runtime extension server: %w", err)
	}

	handlers := []runtimeserver.ExtensionHandler{
		{
			Hook:        runtimehooksv1.CanUpdateMachine,
			Name:        "can-update-machine",
			HandlerFunc: handler.DoCanUpdateMachine,
		},
		{
			Hook:        runtimehooksv1.CanUpdateMachineSet,
			Name:        "can-update-machine-set",
			HandlerFunc: handler.DoCanUpdateMachineSet,
		},
		{
			Hook:        runtimehooksv1.UpdateMachine,
			Name:        "update-machine",
			HandlerFunc: handler.DoUpdateMachine,
		},
	}

	for _, h := range handlers {
		if err := srv.AddExtensionHandler(h); err != nil {
			return fmt.Errorf("failed to register %s handler: %w", h.Name, err)
		}
	}

	if err := mgr.Add(srv); err != nil {
		return fmt.Errorf("failed to add runtime extension server to manager: %w", err)
	}

	return nil
}

// respondFailure records a hook failure on a plain response.
//
// The underlying error is logged rather than returned to Cluster API, which surfaces the
// message on the Machine: it keeps potentially sensitive detail out of the object while
// leaving an operator a clear pointer to the controller logs.
func respondFailure(resp *runtimehooksv1.CommonResponse, message string, err error, log logr.Logger) {
	log.Error(err, message)

	resp.Status = runtimehooksv1.ResponseStatusFailure
	resp.Message = message + ": see the bootstrap provider logs for details"
}

// respondRetryFailure records a hook failure on a retry-capable response.
//
// In-place update failures are deliberately terminal rather than falling back to a rolling
// replacement: once the owning controller has stamped the in-place annotation Cluster API is
// committed to this path, so a failure has to be visible.
func respondRetryFailure(resp *runtimehooksv1.CommonRetryResponse, message string, err error, log logr.Logger) {
	log.Error(err, message)

	resp.Status = runtimehooksv1.ResponseStatusFailure
	resp.Message = message + ": see the bootstrap provider logs for details"
	resp.RetryAfterSeconds = 0
}

// remarshalInto converts a decoded generic value into a typed struct.
func remarshalInto(in any, out any) error {
	if in == nil {
		return fmt.Errorf("value is absent")
	}

	encoded, err := json.Marshal(in)
	if err != nil {
		return err
	}

	return json.Unmarshal(encoded, out)
}

var _ = context.Background
