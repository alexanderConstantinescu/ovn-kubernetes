package ovn

import (
	"fmt"

	egressipv1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
)

const (
	policyAlreadyExistsMsg = "Same routing policy already existed"
)

type egressIPLocal struct {
	egressIPMode
}

func (e *egressIPLocal) addPod(eIP *egressipv1.EgressIP, pod *kapi.Pod) error {
	podIPs, err := e.getPodIPs(pod)
	if err != nil {
		return fmt.Errorf("unable to retrieve pod's: %s/%s IPs, err: %v", pod.Namespace, pod.Name, err)
	}
	for _, status := range eIP.Status {
		mark := util.IPToUint32(status.EgressIP)
		if err := e.createEgressPolicy(podIPs, status, mark); err != nil {
			return fmt.Errorf("unable to create logical router policy for status: %v, err: %v", status, err)
		}
	}
	return nil
}

func (e *egressIPLocal) deletePod(eIP *egressipv1.EgressIP, pod *kapi.Pod) error {
	podIPs, err := e.getPodIPs(pod)
	if err != nil {
		return fmt.Errorf("unable to retrieve pod's: %s/%s IPs, err: %v", pod.Namespace, pod.Name, err)
	}
	for _, status := range eIP.Status {
		if err := e.deleteEgressPolicy(podIPs, status); err != nil {
			return fmt.Errorf("unable to delete logical router policy for status: %v, err: %v", status, err)
		}
	}
	return nil
}
