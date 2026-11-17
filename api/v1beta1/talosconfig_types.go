// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConfigFinalizer allows us to clean up resources before deletion
	ConfigFinalizer = "talosconfig.bootstrap.cluster.x-k8s.io"
)

// TalosConfigSpec defines the desired state of TalosConfig
type TalosConfigSpec struct {
	// Version of Talos to generate machine configuration for, i.e. v1.0 (patch version may be omitted).
	// Defaults to the latest supported version if unspecified.
	// It is recommended to set this field explicitly to avoid unexpected issues during provider upgrades.
	TalosVersion string `json:"talosVersion,omitempty"`

	// Talos machine configuration type to generate: Supported values are:
	// controlplane, worker, init (deprecated), none (configuration must be then provided in data field).
	// +kubebuilder:validation:Enum=controlplane;worker;init;none
	GenerateType string `json:"generateType"`

	// Machine configuration in case generateType=none.
	Data string `json:"data,omitempty"`

	// RFC6902 JSON patches to apply to machine configuration.
	ConfigPatches []ConfigPatches `json:"configPatches,omitempty"`

	// Talos Linux machine configuration strategic merge patch list.
	StrategicPatches []string `json:"strategicPatches,omitempty"`

	// Set hostname in the machine configuration to some value.
	Hostname HostnameSpec `json:"hostname,omitempty"`

	// ImageFactory declares the Talos Image Factory schematic the machine installs and
	// upgrades with. When set, CABPT registers the schematic with the Factory and renders
	// machine.install.image as <factory>/metal-installer/<schematic>:<version>.
	// +optional
	ImageFactory *ImageFactorySpec `json:"imageFactory,omitempty"`

	// RestrictJoin ties the machine's right to join Kubernetes to its owner in Cluster API.
	//
	// The rendered configuration makes the kubelet register its Node with the label
	// bootstrap.cluster.x-k8s.io/talos-join-id set to the UID of the owning Machine (or
	// MachinePool). CABPT keeps an allowlist of the join IDs of living owners in the workload
	// cluster, and a ValidatingAdmissionPolicy there refuses a kubelet that registers a Node
	// with any other ID, so a disk left over from a deleted Machine cannot come back.
	//
	// A worker owned by a Machine also gets a Kubernetes bootstrap token of its own in place
	// of the cluster-wide one. The token only exists in the workload cluster while the Machine
	// lives and its Node is not ready.
	//
	// The policy only refuses registrations once every Machine of the cluster is covered,
	// either by this field or by an existing Node; until then it warns.
	// +optional
	RestrictJoin bool `json:"restrictJoin,omitempty"`
	// Important: Run "make" to regenerate code after modifying this file
}

// ImageFactorySpec mirrors the Talos Image Factory schematic customization.
type ImageFactorySpec struct {
	// Extensions are official system extension names, e.g. siderolabs/nvme-cli. The
	// Factory picks the extension versions matching the Talos version.
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// ExtraKernelArgs are baked into the images the Factory builds from the schematic.
	// +optional
	ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`

	// Overlay selects a single-board-computer overlay.
	// +optional
	Overlay *ImageFactoryOverlay `json:"overlay,omitempty"`

	// Bootloader selects the bootloader of the disk image; the Factory default when empty.
	// +kubebuilder:validation:Enum=auto;dual-boot;grub;sd-boot
	// +optional
	Bootloader string `json:"bootloader,omitempty"`
}

// ImageFactoryOverlay is the Factory overlay block.
type ImageFactoryOverlay struct {
	// Name is the overlay name, e.g. rpi_generic.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Image is the overlay container image, e.g. ghcr.io/siderolabs/sbc-raspberrypi.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
}

// ImageFactoryStatus records the schematic CABPT resolved for this configuration.
type ImageFactoryStatus struct {
	// TalosVersion is the full version the installer image is tagged with.
	// +optional
	TalosVersion string `json:"talosVersion,omitempty"`

	// SchematicID is the content-addressed ID the Factory returned.
	// +optional
	SchematicID string `json:"schematicID,omitempty"`

	// InstallerImage is the machine.install.image rendered into the configuration.
	// +optional
	InstallerImage string `json:"installerImage,omitempty"`

	// ObservedInputs is a hash of spec.talosVersion and spec.imageFactory. While it matches
	// the current spec the fields above are reused without asking the Factory.
	// +optional
	ObservedInputs string `json:"observedInputs,omitempty"`
}

// HostnameSource is the definition of hostname source.
type HostnameSource string

// HostnameSourceMachineName sets the hostname in the generated configuration to the machine name.
const HostnameSourceMachineName HostnameSource = "MachineName"

// HostnameSourceInfrastructureName sets the hostname in the generated configuration to the name of the machine's infrastructure.
const HostnameSourceInfrastructureName HostnameSource = "InfrastructureName"

// HostnameSpec defines the hostname source.
type HostnameSpec struct {
	// Source of the hostname.
	//
	// Allowed values:
	// "MachineName" (use linked Machine's Name).
	// "InfrastructureName" (use linked Machine's infrastructure's name).
	Source HostnameSource `json:"source,omitempty"`
}

// TalosConfigStatus defines the observed state of TalosConfig
type TalosConfigStatus struct {
	// initialization provides observations of the TalosConfig initialization process.
	// NOTE: Fields in this struct are part of the Cluster API contract and are used to orchestrate initial Machine provisioning.
	// +optional
	Initialization TalosConfigInitializationStatus `json:"initialization,omitempty,omitzero"`

	// DataSecretName is the name of the secret that stores the bootstrap data script.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DataSecretName string `json:"dataSecretName,omitempty"`

	// ImageFactory records the schematic resolved from spec.imageFactory.
	// +optional
	ImageFactory *ImageFactoryStatus `json:"imageFactory,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	// +optional
	// kubebuilder:validation:Minimum=1
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the observations of a TalosConfig's current state.
	// Known condition types are Ready, DataSecretAvailable, ClientConfigAvailable.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TalosConfigInitializationStatus provides observations of the TalosConfig initialization process.
// +kubebuilder:validation:MinProperties=1
type TalosConfigInitializationStatus struct {
	// dataSecretCreated is true when the Machine's bootstrap secret is created.
	// NOTE: this field is part of the Cluster API contract, and it is used to orchestrate initial Machine provisioning.
	// +optional
	DataSecretCreated *bool `json:"dataSecretCreated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=talosconfigs,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Initialized",type="string",JSONPath=`.status.initialization.dataSecretCreated`,description="Bootstrap secret is created"
// +kubebuilder:printcolumn:name="Bootstrap secret",type="string",JSONPath=`.status.dataSecretName`,description="Bootstrap secret name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of TalosConfig"

// TalosConfig is the Schema for the talosconfigs API
type TalosConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosConfigSpec   `json:"spec,omitempty"`
	Status TalosConfigStatus `json:"status,omitempty"`
}

// GetConditions returns the set of conditions for this object.
func (c *TalosConfig) GetConditions() []metav1.Condition {
	return c.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (c *TalosConfig) SetConditions(conditions []metav1.Condition) {
	c.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// TalosConfigList contains a list of TalosConfig
type TalosConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosConfig `json:"items"`
}

func init() {
	register(&TalosConfig{}, &TalosConfigList{})
}
