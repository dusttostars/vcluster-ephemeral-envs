package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "ephemeral.io"
const Version = "v1alpha1"

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}

type EphemeralEnvironmentSpec struct {
	Tenant   string            `json:"tenant"`
	Branch   string            `json:"branch"`
	TTL      string            `json:"ttl,omitempty"`
	App      AppSpec           `json:"app"`
	Labels   map[string]string `json:"labels,omitempty"`
}

type AppSpec struct {
	Image    string            `json:"image"`
	Replicas int32             `json:"replicas,omitempty"`
	Port     int32             `json:"port,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

type EphemeralEnvironmentStatus struct {
	Phase      string      `json:"phase,omitempty"`
	ExpiresAt  metav1.Time `json:"expiresAt,omitempty"`
	VClusterApp string     `json:"vclusterApp,omitempty"`
	Message    string      `json:"message,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type EphemeralEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EphemeralEnvironmentSpec   `json:"spec"`
	Status EphemeralEnvironmentStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type EphemeralEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EphemeralEnvironment `json:"items"`
}

func (in *EphemeralEnvironment) DeepCopyInto(out *EphemeralEnvironment) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *EphemeralEnvironment) DeepCopy() *EphemeralEnvironment {
	if in == nil {
		return nil
	}
	out := new(EphemeralEnvironment)
	in.DeepCopyInto(out)
	return out
}

func (in *EphemeralEnvironment) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *EphemeralEnvironmentList) DeepCopyInto(out *EphemeralEnvironmentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]EphemeralEnvironment, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *EphemeralEnvironmentList) DeepCopy() *EphemeralEnvironmentList {
	if in == nil {
		return nil
	}
	out := new(EphemeralEnvironmentList)
	in.DeepCopyInto(out)
	return out
}

func (in *EphemeralEnvironmentList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

func (in *EphemeralEnvironmentSpec) DeepCopyInto(out *EphemeralEnvironmentSpec) {
	*out = *in
	in.App.DeepCopyInto(&out.App)
	if in.Labels != nil {
		out.Labels = make(map[string]string, len(in.Labels))
		for k, v := range in.Labels {
			out.Labels[k] = v
		}
	}
}

func (in *AppSpec) DeepCopyInto(out *AppSpec) {
	*out = *in
	if in.Env != nil {
		out.Env = make(map[string]string, len(in.Env))
		for k, v := range in.Env {
			out.Env[k] = v
		}
	}
}

func (in *EphemeralEnvironmentStatus) DeepCopyInto(out *EphemeralEnvironmentStatus) {
	*out = *in
	in.ExpiresAt.DeepCopyInto(&out.ExpiresAt)
}
