package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Until this gets in https://github.com/kubernetes-sigs/controller-tools/pull/455 we need
// to manually manage that Scope: Cluster is set. Once that is done, bump controller-gen and set the output of
// ./hack/update-code-gen.sh to ~/dist/templates/ so that we can monitor changes to CRDs which are
//  automatically generated

// Again, when we bump to Kubernetes 1.19 we should get this fix: https://github.com/kubernetes/kubernetes/pull/89660
// Until then Assigned Nodes/EgressIPs can only print the first item in the status.

// +genclient
// +genclient:nonNamespaced
// +genclient:noStatus
// +resource:path=egressip
// +kubebuilder:resource:shortName=eip
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:printcolumn:name="EgressIPs",type=string,JSONPath=".spec.egressIPs[*]"
// +kubebuilder:printcolumn:name="Assigned Node",type=string,JSONPath=".status[*].node"
// +kubebuilder:printcolumn:name="Assigned EgressIPs",type=string,JSONPath=".status[*].egressIP"
// EgressIP is a CRD allowing the user to define a fixed
// source IP for all egress traffic originating from any pods which
// match the EgressIP resource according to its spec definition.
type EgressIP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired behavior of EgressIP.
	Spec EgressIPSpec `json:"spec"`
	// Observed status of EgressIP. Read-only.
	// +optional
	Status []EgressIPStatus `json:"status,omitempty"`
}

// EgressIPStatus is the per node EgressIP status, for those who have been assigned one.
// This is to be treated as read-only.
type EgressIPStatus struct {
	Node     string `json:"node"`
	EgressIP string `json:"egressIP"`
}

// EgressIPSpec is a desired state description of EgressIP.
type EgressIPSpec struct {
	// EgressIPs is the list of egress IP addresses requested. Can be IPv4 and/or IPv6.
	// This field is mandatory.
	EgressIPs []string `json:"egressIPs"`
	// PodSelector applies the egress IP only to the pods whos label
	// matches this definition. This field is optional, an in case it's not set
	// results in the egress IP being applied to all pods in the namespace(s)
	// matching the NamespaceSelector. In case it is set: it acts in an additive way with
	// the NamespaceSelector, and results in the EgressIP being applied to the pods
	// matching this label, and which already matched the NamespaceSelector.
	// +optional
	PodSelector metav1.LabelSelector `json:"podSelector,omitempty"`
	// NamespaceSelector applies the egress IP only to the namespace whos label
	// matches this definition. This field is mandatory.
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=egressip
// EgressIPList is the list of EgressIPList.
type EgressIPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of EgressIP.
	Items []EgressIP `json:"items"`
}
