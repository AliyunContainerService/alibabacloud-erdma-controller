/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ERdmaDeviceSpec defines the desired state of ERdmaDevice
type ERdmaDeviceSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of ERdmaDevice. Edit erdmadevice_types.go to remove/update
	InstanceID       string `json:"instanceID,omitempty"`
	MAC              string `json:"mac,omitempty"`
	IsPrimaryENI     bool   `json:"isPrimaryENI,omitempty"`
	ID               string `json:"id,omitempty"`
	NetworkCardIndex int    `json:"networkCardIndex,omitempty"`
}

// ERdmaDeviceStatus defines the observed state of ERdmaDevice
type ERdmaDeviceStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=erdmadevices,scope=Cluster
// +kubebuilder:subresource:status

// ERdmaDevice is the Schema for the erdmadevices API
type ERdmaDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ERdmaDeviceSpec   `json:"spec,omitempty"`
	Status ERdmaDeviceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ERdmaDeviceList contains a list of ERdmaDevice
type ERdmaDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ERdmaDevice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ERdmaDevice{}, &ERdmaDeviceList{})
}
