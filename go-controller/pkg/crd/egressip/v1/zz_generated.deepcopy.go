// +build !ignore_autogenerated

/*
Copyright The Kubernetes Authors.

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

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EgressIP) DeepCopyInto(out *EgressIP) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	if in.Status != nil {
		in, out := &in.Status, &out.Status
		*out = make([]EgressIPStatus, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EgressIP.
func (in *EgressIP) DeepCopy() *EgressIP {
	if in == nil {
		return nil
	}
	out := new(EgressIP)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *EgressIP) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EgressIPList) DeepCopyInto(out *EgressIPList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]EgressIP, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EgressIPList.
func (in *EgressIPList) DeepCopy() *EgressIPList {
	if in == nil {
		return nil
	}
	out := new(EgressIPList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *EgressIPList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EgressIPSpec) DeepCopyInto(out *EgressIPSpec) {
	*out = *in
	if in.EgressIPs != nil {
		in, out := &in.EgressIPs, &out.EgressIPs
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	in.PodSelector.DeepCopyInto(&out.PodSelector)
	in.NamespaceSelector.DeepCopyInto(&out.NamespaceSelector)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EgressIPSpec.
func (in *EgressIPSpec) DeepCopy() *EgressIPSpec {
	if in == nil {
		return nil
	}
	out := new(EgressIPSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *EgressIPStatus) DeepCopyInto(out *EgressIPStatus) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new EgressIPStatus.
func (in *EgressIPStatus) DeepCopy() *EgressIPStatus {
	if in == nil {
		return nil
	}
	out := new(EgressIPStatus)
	in.DeepCopyInto(out)
	return out
}
