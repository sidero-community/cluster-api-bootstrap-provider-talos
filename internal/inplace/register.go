// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package inplace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
)

// RegistrarOptions configures the ExtensionConfig registrar.
type RegistrarOptions struct {
	// Name of the ExtensionConfig to maintain.
	Name string

	// ServiceName and ServiceNamespace locate the Service fronting this server.
	ServiceName      string
	ServiceNamespace string

	// ServicePort is the port the Service listens on.
	ServicePort int32

	// CACertPath is the CA certificate presented to Cluster API, written alongside the serving
	// certificate by cert-manager.
	CACertPath string

	// ResyncPeriod is how often the ExtensionConfig is re-checked, so a rotated CA is picked up.
	ResyncPeriod time.Duration
}

// Registrar keeps the ExtensionConfig that points Cluster API at this server correct.
//
// The registration cannot be shipped as a static manifest. ExtensionConfig is cluster scoped, so
// neither clusterctl nor the Cluster API operator rewrites the namespace inside
// spec.clientConfig.service the way they do for webhook configurations, and a manifest built for
// one namespace points at a Service that does not exist when installed into another. The CA has
// the same problem from the other direction: cert-manager's ca-injector only patches webhook
// configurations, APIServices and CRDs, so cert-manager.io/inject-ca-from on an ExtensionConfig
// is silently a no-op and the caBundle stays empty.
//
// Both values are known here at runtime: the namespace comes from the downward API and the CA
// from the mounted serving certificate. Writing the object from the manager is what makes the
// extension work regardless of the namespace it was installed into.
//
// Getting this wrong is not a soft failure. Cluster API's registry warmup treats a
// non-discoverable ExtensionConfig as fatal, so a stale one crash-loops the core manager.
type Registrar struct {
	client client.Client
	opts   RegistrarOptions
}

// NewRegistrar builds a Registrar writing the ExtensionConfig through c.
func NewRegistrar(c client.Client, opts RegistrarOptions) *Registrar {
	if opts.ResyncPeriod == 0 {
		opts.ResyncPeriod = 10 * time.Minute
	}

	return &Registrar{client: c, opts: opts}
}

// NeedLeaderElection makes the registrar run only on the leader, so replicas do not race to write
// the same object.
func (r *Registrar) NeedLeaderElection() bool {
	return true
}

// Start implements manager.Runnable.
func (r *Registrar) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("extensionconfig-registrar")

	ticker := time.NewTicker(r.opts.ResyncPeriod)
	defer ticker.Stop()

	for {
		if err := r.ensure(ctx); err != nil {
			// Never fail the Runnable: a failure here takes the whole manager down with it, and
			// the extension not being registered is recoverable on the next tick.
			log.Error(err, "failed to register the in-place update ExtensionConfig, retrying",
				"name", r.opts.Name, "retryIn", r.opts.ResyncPeriod)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ensure creates or updates the ExtensionConfig so it points at this server.
func (r *Registrar) ensure(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("extensionconfig-registrar")

	caBundle, err := os.ReadFile(filepath.Clean(r.opts.CACertPath))
	if err != nil {
		return fmt.Errorf("reading CA certificate %q: %w", r.opts.CACertPath, err)
	}

	if len(caBundle) == 0 {
		return fmt.Errorf("CA certificate %q is empty", r.opts.CACertPath)
	}

	existing := &runtimev1.ExtensionConfig{}

	err = r.client.Get(ctx, types.NamespacedName{Name: r.opts.Name}, existing)

	switch {
	case errors.IsNotFound(err):
		desired := &runtimev1.ExtensionConfig{
			ObjectMeta: metav1.ObjectMeta{Name: r.opts.Name},
		}
		r.apply(desired, caBundle)

		if err := r.client.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating ExtensionConfig: %w", err)
		}

		log.Info("registered the in-place update ExtensionConfig",
			"name", r.opts.Name, "service", r.opts.ServiceName, "namespace", r.opts.ServiceNamespace)

		return nil
	case meta.IsNoMatchError(err):
		// The RuntimeSDK feature gate is off on the core controllers, so the CRD is not served.
		// Serving the hooks is harmless in that state; there is simply nothing to register with.
		log.V(4).Info("ExtensionConfig CRD is not served, skipping registration; " +
			"enable the RuntimeSDK feature gate on the core Cluster API controllers to use in-place updates")

		return nil
	case err != nil:
		return fmt.Errorf("reading ExtensionConfig: %w", err)
	}

	before := existing.DeepCopy()
	r.apply(existing, caBundle)

	if equalClientConfig(before, existing) {
		return nil
	}

	if err := r.client.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating ExtensionConfig: %w", err)
	}

	log.Info("corrected the in-place update ExtensionConfig",
		"name", r.opts.Name, "service", r.opts.ServiceName, "namespace", r.opts.ServiceNamespace)

	return nil
}

// apply writes the desired client configuration onto ec, leaving everything else, notably any
// namespaceSelector an operator has narrowed, untouched.
func (r *Registrar) apply(ec *runtimev1.ExtensionConfig, caBundle []byte) {
	ec.Spec.ClientConfig.CABundle = caBundle
	ec.Spec.ClientConfig.URL = ""
	port := r.opts.ServicePort
	ec.Spec.ClientConfig.Service = runtimev1.ServiceReference{
		Name:      r.opts.ServiceName,
		Namespace: r.opts.ServiceNamespace,
		Port:      &port,
	}
}

// equalClientConfig reports whether the client configuration of a and b already match.
func equalClientConfig(a, b *runtimev1.ExtensionConfig) bool {
	if string(a.Spec.ClientConfig.CABundle) != string(b.Spec.ClientConfig.CABundle) {
		return false
	}

	as, bs := a.Spec.ClientConfig.Service, b.Spec.ClientConfig.Service
	if as.Name != bs.Name || as.Namespace != bs.Namespace {
		return false
	}

	switch {
	case as.Port == nil && bs.Port == nil:
		return true
	case as.Port == nil || bs.Port == nil:
		return false
	default:
		return *as.Port == *bs.Port
	}
}
