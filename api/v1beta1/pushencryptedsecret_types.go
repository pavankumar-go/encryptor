/*
Copyright 2023.

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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	STORE_FAILED  = "Failed to store"
	STORE_SUCCESS = "Successfully synced to Parameter store"
	MESSAGE_OK    = "ALLOK"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PushEncryptedSecretSpec defines the desired state of PushEncryptedSecret
type PushEncryptedSecretSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of PushEncryptedSecret. Edit pushencryptedsecret_types.go to remove/update
	Data []PushEncryptedSecretData `json:"data"`
}

// PushEncryptedSecretSpec defines the desired state of PushEncryptedSecret
type PushEncryptedSecretData struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of PushEncryptedSecret. Edit pushencryptedsecret_types.go to remove/update
	EncryptedSecret string `json:"encryptedSecret"`
	RemoteRefKey    string `json:"remoteRefKey"`
}

// PushEncryptedSecretStatus defines the observed state of PushEncryptedSecret
type PushEncryptedSecretStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Status           string      `json:"status,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	Hash             string      `json:"hash,omitempty"`
	ErrorOnOperation string      `json:"errorOnOperation,omitempty"`
	LastUpdate       metav1.Time `json:"lastUpdate,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="STATUS",type="string",JSONPath=`.status.status`

// PushEncryptedSecret is the Schema for the pushencryptedsecrets API
type PushEncryptedSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PushEncryptedSecretSpec   `json:"spec,omitempty"`
	Status PushEncryptedSecretStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PushEncryptedSecretList contains a list of PushEncryptedSecret
type PushEncryptedSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PushEncryptedSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PushEncryptedSecret{}, &PushEncryptedSecretList{})
}
