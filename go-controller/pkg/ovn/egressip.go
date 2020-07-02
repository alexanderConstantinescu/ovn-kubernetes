package ovn

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	goovn "github.com/ebay/go-ovn"
	egressipv1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	utilnet "k8s.io/utils/net"
)

type modeEgressIP interface {
	addPod(eIP *egressipv1.EgressIP, pod *kapi.Pod) error
	deletePod(eIP *egressipv1.EgressIP, pod *kapi.Pod) error
	needsRetry(pod *kapi.Pod) bool
}

func (oc *Controller) addEgressIP(eIP *egressipv1.EgressIP) error {
	// If the status is set at this point, then we know it's valid from syncEgressIP and we have no assignment to do.
	// Just initialize all watchers (which should not re-create any already existing items in the OVN DB)
	if len(eIP.Status) == 0 {
		assignments, err := oc.assignEgressIPs(eIP)
		if err != nil {
			klog.Errorf("Unable to assign egress IP: %s, error: %v", eIP.Name, err)
		}
		eIP.Status = assignments
	}

	oc.egressIPNamespaceHandlerMutex.Lock()
	defer oc.egressIPNamespaceHandlerMutex.Unlock()

	h, err := oc.watchFactory.AddFilteredNamespaceHandler("", &eIP.Spec.NamespaceSelector,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				namespace := obj.(*kapi.Namespace)
				klog.V(5).Infof("EgressIP: %s has matched on namespace: %s", eIP.Name, namespace.Name)
				if err := oc.addNamespaceEgressIP(eIP, namespace); err != nil {
					klog.Errorf("error: unable to add namespace handler for EgressIP: %s, err: %v", eIP.Name, err)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {},
			DeleteFunc: func(obj interface{}) {
				namespace := obj.(*kapi.Namespace)
				klog.V(5).Infof("EgressIP: %s stopped matching on namespace: %s", eIP.Name, namespace.Name)
				if err := oc.deleteNamespaceEgressIP(eIP, namespace); err != nil {
					klog.Errorf("error: unable to delete namespace handler for EgressIP: %s, err: %v", eIP.Name, err)
				}
			},
		}, nil)
	if err != nil {
		return err
	}
	oc.egressIPNamespaceHandlerCache[getEgressIPKey(eIP)] = *h

	if err := oc.updateEgressIPWithRetry(eIP); err != nil {
		return err
	}

	return nil
}

func (oc *Controller) deleteEgressIP(eIP *egressipv1.EgressIP) error {
	if err := oc.releaseEgressIPs(eIP); err != nil {
		return err
	}

	oc.egressIPNamespaceHandlerMutex.Lock()
	defer oc.egressIPNamespaceHandlerMutex.Unlock()
	delete(oc.egressIPNamespaceHandlerCache, getEgressIPKey(eIP))

	oc.egressIPPodHandlerMutex.Lock()
	defer oc.egressIPPodHandlerMutex.Unlock()
	delete(oc.egressIPPodHandlerCache, getEgressIPKey(eIP))

	namespaces, err := oc.kube.GetNamespaces(eIP.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	for _, namespace := range namespaces.Items {
		if err := oc.deleteAllEgressPods(eIP, &namespace); err != nil {
			return err
		}
	}
	return nil
}

func (oc *Controller) syncEgressIPs(eIPs []interface{}) {
	oc.eIPAllocatorMutex.Lock()
	defer oc.eIPAllocatorMutex.Unlock()
	for _, eIP := range eIPs {
		eIP, ok := eIP.(*egressipv1.EgressIP)
		if !ok {
			klog.Errorf("Spurious object in syncEgressIPs: %v", eIP)
			continue
		}
		validAssignment := true
		for _, eIPStatus := range eIP.Status {
			validAssignment = false
			eNode, exists := oc.eIPAllocator[eIPStatus.Node]
			if !exists {
				klog.Errorf("Allocator error: EgressIP: %s claims to have an allocation on a node which is unassignable for egress IP: %s", eIP.Name, eIPStatus.Node)
				break
			}
			if !eNode.tainted {
				ip := net.ParseIP(eIPStatus.EgressIP)
				if ip == nil {
					klog.Errorf("Allocator error: EgressIP allocation contains unparsable IP address: %s", eIPStatus.EgressIP)
					break
				}
				if utilnet.IsIPv6(ip) && eNode.v6Subnet != nil {
					if !eNode.v6Subnet.Contains(ip) {
						klog.Errorf("Allocator error: EgressIP allocation: %s on subnet: %s which cannot host it", ip.String(), eNode.v6Subnet.String())
						break
					}
				} else if !utilnet.IsIPv6(ip) && eNode.v4Subnet != nil {
					if !eNode.v4Subnet.Contains(ip) {
						klog.Errorf("Allocator error: EgressIP allocation: %s on subnet: %s which cannot host it", ip.String(), eNode.v4Subnet.String())
						break
					}
				} else {
					klog.Errorf("Allocator error: EgressIP allocation on node: %s which does not support its IP protocol version", eIPStatus.Node)
					break
				}
				validAssignment = true
			} else {
				klog.Errorf("Allocator error: EgressIP: %s claims multiple egress IPs on same node: %s, will attempt rebalancing", eIP.Name, eIPStatus.Node)
				break
			}
			eNode.tainted = true
		}
		// In the unlikely event that something has been missallocated previously:
		// unassign that by updating the status and re-allocate properely in addEgressIP
		if !validAssignment {
			eIP.Status = []egressipv1.EgressIPStatus{}
			if err := oc.updateEgressIPWithRetry(eIP); err != nil {
				klog.Error(err)
				continue
			}
			namespaces, err := oc.kube.GetNamespaces(eIP.Spec.NamespaceSelector)
			if err != nil {
				klog.Errorf("Unable to list namespaces matched by EgressIP: %s, err: %v", getEgressIPKey(eIP), err)
				continue
			}
			for _, namespace := range namespaces.Items {
				if err := oc.deleteAllEgressPods(eIP, &namespace); err != nil {
					klog.Error(err)
				}
			}
		}
		for _, eNode := range oc.eIPAllocator {
			eNode.tainted = false
		}
	}
}

func (oc *Controller) updateEgressIPWithRetry(eIP *egressipv1.EgressIP) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return oc.kube.UpdateEgressIP(eIP)
	})
}

func (oc *Controller) addNamespaceEgressIP(eIP *egressipv1.EgressIP, namespace *kapi.Namespace) error {
	oc.egressIPPodHandlerMutex.Lock()
	defer oc.egressIPPodHandlerMutex.Unlock()
	h, err := oc.watchFactory.AddFilteredPodHandler(namespace.Name, &eIP.Spec.PodSelector,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod := obj.(*kapi.Pod)
				klog.V(5).Infof("EgressIP: %s has matched on pod: %s in namespace: %s", eIP.Name, pod.Name, namespace.Name)
				if err := oc.modeEgressIP.addPod(eIP, pod); err != nil {
					klog.Errorf("Unable to add pod: %s/%s to EgressIP: %s, err: %v", pod.Namespace, pod.Name, eIP.Name, err)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				newPod := newObj.(*kapi.Pod)
				// FYI: the only pod update we care about here is the pod being assigned an IP, which
				// it didn't have when we received the ADD. If the label is changed and it stops matching:
				// this watcher receives a delete.
				klog.V(5).Infof("EgressIP: %s update for pod: %s in namespace: %s, needs retry: %v", eIP.Name, newPod.Name, namespace.Name, oc.modeEgressIP.needsRetry(newPod))
				if oc.modeEgressIP.needsRetry(newPod) {
					if err := oc.modeEgressIP.addPod(eIP, newPod); err != nil {
						klog.Errorf("Unable to add pod: %s/%s to EgressIP: %s, err: %v", newPod.Namespace, newPod.Name, eIP.Name, err)
					}
				}
			},
			DeleteFunc: func(obj interface{}) {
				pod := obj.(*kapi.Pod)
				// FYI: we can be in a situation where we processed a pod ADD for which there was no IP
				// address assigned. If the pod is deleted before the IP address is assigned,
				// we should not process that delete (as nothing exists in OVN for it)
				klog.V(5).Infof("EgressIP: %s has stopped matching on pod: %s in namespace: %s, needs delete: %v", eIP.Name, pod.Name, namespace.Name, !oc.modeEgressIP.needsRetry(pod))
				if !oc.modeEgressIP.needsRetry(pod) {
					if err := oc.modeEgressIP.deletePod(eIP, pod); err != nil {
						klog.Errorf("Unable to delete pod: %s/%s to EgressIP: %s, err: %v", pod.Namespace, pod.Name, eIP.Name, err)
					}
				}
			},
		}, nil)
	if err != nil {
		return err
	}
	oc.egressIPPodHandlerCache[getEgressIPKey(eIP)] = *h
	return nil
}

func (oc *Controller) deleteNamespaceEgressIP(eIP *egressipv1.EgressIP, namespace *kapi.Namespace) error {
	oc.egressIPPodHandlerMutex.Lock()
	defer oc.egressIPPodHandlerMutex.Unlock()
	delete(oc.egressIPPodHandlerCache, getEgressIPKey(eIP))
	if err := oc.deleteAllEgressPods(eIP, namespace); err != nil {
		return err
	}
	return nil
}

func (oc *Controller) deleteAllEgressPods(eIP *egressipv1.EgressIP, namespace *kapi.Namespace) error {
	pods, err := oc.kube.GetPods(namespace.Name, eIP.Spec.PodSelector)
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if !oc.modeEgressIP.needsRetry(&pod) {
			if err := oc.modeEgressIP.deletePod(eIP, &pod); err != nil {
				return err
			}
		}
	}
	return nil
}

func (oc *Controller) assignEgressIPs(eIP *egressipv1.EgressIP) ([]egressipv1.EgressIPStatus, error) {
	oc.eIPAllocatorMutex.Lock()
	defer oc.eIPAllocatorMutex.Unlock()

	if len(oc.eIPAllocator) == 0 {
		oc.egressAssignmentRetry.Store(eIP, true)
		eIPRef := kapi.ObjectReference{
			Kind:      "EgressIP",
			Namespace: eIP.Namespace,
			Name:      eIP.Name,
		}
		oc.recorder.Eventf(&eIPRef, kapi.EventTypeWarning, "NoMatchingNodeFound", "no assignable nodes for EgressIP: %s, please tag at least one node with label: %s", eIP.Name, util.GetNodeEgressLabel())
		return nil, fmt.Errorf("no assignable nodes")
	}

	assignments := []egressipv1.EgressIPStatus{}
	eNodes, existingAllocations := oc.getSortedEgressData()
	klog.V(5).Infof("Current assignments are: %+v", existingAllocations)
	for _, egressIP := range eIP.Spec.EgressIPs {
		klog.V(5).Infof("Will attempt assignment for egress IP: %s", egressIP)
		eIPC := net.ParseIP(egressIP)
		if eIPC == nil {
			eIPRef := kapi.ObjectReference{
				Kind:      "EgressIP",
				Namespace: eIP.Namespace,
				Name:      eIP.Name,
			}
			oc.recorder.Eventf(&eIPRef, kapi.EventTypeWarning, "InvalidEgressIP", "egress IP: %s for object EgressIP: %s is not a valid IP address", egressIP, eIP.Name)
			klog.Errorf("Unable to parse provided EgressIP: %s, invalid", egressIP)
			continue
		}
		for i := 0; i < len(eNodes); i++ {
			klog.V(5).Infof("Attempting assignment on egress node: %+v", eNodes[i])
			if _, exists := existingAllocations[eIPC.String()]; !exists {
				if !eNodes[i].tainted {
					if (utilnet.IsIPv6(eIPC) && eNodes[i].v6Subnet != nil && eNodes[i].v6Subnet.Contains(eIPC)) ||
						(!utilnet.IsIPv6(eIPC) && eNodes[i].v4Subnet != nil && eNodes[i].v4Subnet.Contains(eIPC)) {
						eNodes[i].tainted, oc.eIPAllocator[eNodes[i].name].allocations[eIPC.String()] = true, true
						assignments = append(assignments, egressipv1.EgressIPStatus{
							EgressIP: eIPC.String(),
							Node:     eNodes[i].name,
						})
						klog.V(5).Infof("Successful assignment of egress IP: %s on node: %+v", egressIP, eNodes[i])
						break
					}
				}
			}
		}
	}
	if len(assignments) == 0 {
		eIPRef := kapi.ObjectReference{
			Kind:      "EgressIP",
			Namespace: eIP.Namespace,
			Name:      eIP.Name,
		}
		oc.recorder.Eventf(&eIPRef, kapi.EventTypeWarning, "NoMatchingNodeFound", "No matching nodes found, which can host any of the egress IPs: %v for object EgressIP: %s", eIP.Spec.EgressIPs, eIP.Name)
		return nil, fmt.Errorf("no matching host found")
	}
	return assignments, nil
}

func (oc *Controller) releaseEgressIPs(eIP *egressipv1.EgressIP) error {
	oc.eIPAllocatorMutex.Lock()
	defer oc.eIPAllocatorMutex.Unlock()
	for _, status := range eIP.Status {
		klog.V(5).Infof("Releasing egress IP assignment: %s", status.EgressIP)
		if node, exists := oc.eIPAllocator[status.Node]; exists {
			delete(node.allocations, status.EgressIP)
		}
		klog.V(5).Infof("Remaining allocations on node are: %+v", oc.eIPAllocator[status.Node])
	}
	return nil
}

func (oc *Controller) getSortedEgressData() ([]eNode, map[string]bool) {
	nodes := []eNode{}
	allAllocations := make(map[string]bool)
	for _, eNode := range oc.eIPAllocator {
		nodes = append(nodes, *eNode)
		for ip := range eNode.allocations {
			allAllocations[ip] = true
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return len(nodes[i].allocations) < len(nodes[j].allocations)
	})
	return nodes, allAllocations
}

func getEgressIPKey(eIP *egressipv1.EgressIP) string {
	return eIP.Name
}

func getPodKey(pod *kapi.Pod) string {
	return fmt.Sprintf("%s_%s", pod.Namespace, pod.Name)
}

type egressIPMode struct {
	ovnNBClient    goovn.Client
	podRetry       sync.Map
	gatewayIPCache sync.Map
}

type gatewayRouter struct {
	ipV4 net.IP
	ipV6 net.IP
}

func (e *egressIPMode) getGatewayRouterIP(node string, wantsIPv6 bool) (net.IP, error) {
	if item, exists := e.gatewayIPCache.Load(node); exists {
		if gatewayRouter, ok := item.(gatewayRouter); ok {
			if wantsIPv6 && gatewayRouter.ipV6 != nil {
				return gatewayRouter.ipV6, nil
			} else if !wantsIPv6 && gatewayRouter.ipV4 != nil {
				return gatewayRouter.ipV4, nil
			}
			return nil, fmt.Errorf("node does not have a gateway router IP corresponding to the IP version requested")
		}
		return nil, fmt.Errorf("unable to cast node: %s gatewayIP cache item to correct type", node)
	}
	gatewayRouter := gatewayRouter{}
	err := utilwait.ExponentialBackoff(retry.DefaultRetry, func() (bool, error) {
		grIPv4, grIPv6, err := util.GetNodeGatewayRouterAddress(node)
		if err != nil {
			return false, nil
		}
		if grIPv4 != nil {
			gatewayRouter.ipV4 = grIPv4
		}
		if grIPv6 != nil {
			gatewayRouter.ipV6 = grIPv6
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	e.gatewayIPCache.Store(node, gatewayRouter)
	if wantsIPv6 && gatewayRouter.ipV6 != nil {
		return gatewayRouter.ipV6, nil
	} else if !wantsIPv6 && gatewayRouter.ipV4 != nil {
		return gatewayRouter.ipV4, nil
	}
	return nil, fmt.Errorf("node does not have a gateway router IP corresponding to the IP version requested")
}

func (e *egressIPMode) getPodIPs(pod *kapi.Pod) ([]net.IP, error) {
	_, podIps, err := util.GetPortAddresses(getPodKey(pod), e.ovnNBClient)
	if err != nil || len(podIps) == 0 {
		e.podRetry.Store(getPodKey(pod), true)
		return nil, fmt.Errorf("pod does not have IP assigned, err: %v", err)
	}
	if e.needsRetry(pod) {
		e.podRetry.Delete(getPodKey(pod))
	}
	return podIps, nil
}

func (e *egressIPMode) needsRetry(pod *kapi.Pod) bool {
	_, retry := e.podRetry.Load(getPodKey(pod))
	return retry
}

func (e *egressIPMode) createEgressPolicy(podIps []net.IP, status egressipv1.EgressIPStatus, mark uint32) error {
	isEgressIPv6 := utilnet.IsIPv6String(status.EgressIP)
	gatewayRouterIP, err := e.getGatewayRouterIP(status.Node, isEgressIPv6)
	if err != nil {
		return fmt.Errorf("unable to retrieve node's: %s gateway IP, err: %v", status.Node, err)
	}
	for _, podIP := range podIps {
		if isEgressIPv6 && utilnet.IsIPv6(podIP) {
			var stderr string
			var err error
			if mark == 0 {
				_, stderr, err = util.RunOVNNbctl("lr-policy-add", ovnClusterRouter, "100",
					fmt.Sprintf("ip6.src == %s", podIP.String()), "reroute", gatewayRouterIP.String())
			} else {
				_, stderr, err = util.RunOVNNbctl("lr-policy-add", ovnClusterRouter, "100",
					fmt.Sprintf("ip6.src == %s", podIP.String()), "reroute", gatewayRouterIP.String(), fmt.Sprintf("pkt_mark=%v", mark))
			}
			if err != nil && !strings.Contains(stderr, policyAlreadyExistsMsg) {
				return fmt.Errorf("unable to create logical router policy for egress IP: %s, stderr: %s, err: %v", status.EgressIP, stderr, err)
			}
		} else if !isEgressIPv6 && !utilnet.IsIPv6(podIP) {
			var stderr string
			var err error
			if mark == 0 {
				_, stderr, err = util.RunOVNNbctl("lr-policy-add", ovnClusterRouter, "100",
					fmt.Sprintf("ip4.src == %s", podIP.String()), "reroute", gatewayRouterIP.String())
			} else {
				_, stderr, err = util.RunOVNNbctl("lr-policy-add", ovnClusterRouter, "100",
					fmt.Sprintf("ip4.src == %s", podIP.String()), "reroute", gatewayRouterIP.String(), fmt.Sprintf("pkt_mark=%v", mark))
			}
			if err != nil && !strings.Contains(stderr, policyAlreadyExistsMsg) {
				return fmt.Errorf("unable to create logical router policy for egress IP: %s, stderr: %s, err: %v", status.EgressIP, stderr, err)
			}
		}
	}
	return nil
}

func (e *egressIPMode) deleteEgressPolicy(podIps []net.IP, status egressipv1.EgressIPStatus) error {
	for _, podIP := range podIps {
		if utilnet.IsIPv6(podIP) && utilnet.IsIPv6String(status.EgressIP) {
			_, _, err := util.RunOVNNbctl("lr-policy-del", ovnClusterRouter, "100", fmt.Sprintf("ip6.src == %s", podIP.String()))
			if err != nil {
				return fmt.Errorf("unable to delete logical router policy for pod IP: %s, err: %v", podIP.String(), err)
			}
		} else if !utilnet.IsIPv6(podIP) && !utilnet.IsIPv6String(status.EgressIP) {
			_, _, err := util.RunOVNNbctl("lr-policy-del", ovnClusterRouter, "100", fmt.Sprintf("ip4.src == %s", podIP.String()))
			if err != nil {
				return fmt.Errorf("unable to delete logical router policy for pod IP: %s, err: %v", podIP.String(), err)
			}
		}
	}
	return nil
}

func (oc *Controller) addEgressNode(egressNode *kapi.Node) error {
	klog.V(5).Infof("Egress node: %s about to be initialized", egressNode.Name)
	if err := oc.initEgressIPAllocator(egressNode); err != nil {
		return fmt.Errorf("egress node initialization error: %v", err)
	}
	oc.egressAssignmentRetry.Range(func(key, value interface{}) bool {
		eIP := key.(*egressipv1.EgressIP)
		klog.V(5).Infof("Assignment for EgressIP: %s attempted by new node: %s", eIP.Name, egressNode.Name)
		assignments, err := oc.assignEgressIPs(eIP)
		if err != nil {
			klog.Errorf("Unable to re-assign egress IP: %s on new egress node: %s, err: %v", eIP.Name, egressNode.Name, err)
			return true
		}
		eIP.Status = assignments
		if err := oc.updateEgressIPWithRetry(eIP); err != nil {
			klog.Errorf("Unable to update newly assigned egress IP: %s, err: %v", eIP.Name, err)
			return true
		}
		oc.egressAssignmentRetry.Delete(eIP)
		return true
	})
	return nil
}

func (oc *Controller) deleteEgressNode(egressNode *kapi.Node) error {
	klog.V(5).Infof("Egress node: %s about to be removed", egressNode.Name)

	oc.eIPAllocatorMutex.Lock()
	delete(oc.eIPAllocator, egressNode.Name)
	oc.eIPAllocatorMutex.Unlock()

	egressIPs, err := oc.kube.GetEgressIPs()
	if err != nil {
		return fmt.Errorf("unable to list egressIPs, err: %v", err)
	}
	for _, eIP := range egressIPs.Items {
		needsReassignment := false
		for _, status := range eIP.Status {
			if status.Node == egressNode.Name {
				needsReassignment = true
			}
		}
		if needsReassignment {
			klog.V(5).Infof("EgressIP: %s about to be re-assigned", eIP.Name)
			assignments, err := oc.assignEgressIPs(&eIP)
			if err != nil {
				klog.Errorf("Unable to re-assign egress IP: %s to a new egress node, err: %v", eIP.Name, err)
				oc.egressAssignmentRetry.Store(&eIP, true)
			}
			eIP.Status = assignments
			if err := oc.updateEgressIPWithRetry(&eIP); err != nil {
				klog.Errorf("Unable to update newly assigned egress IP: %s, err: %v", eIP.Name, err)
			}
		}
	}
	return nil
}

func (oc *Controller) syncEgressNodes(egressNodes []interface{}) {
	for _, egressNode := range egressNodes {
		egressNode, ok := egressNode.(*kapi.Node)
		if !ok {
			klog.Errorf("Spurious object in syncEgressNodes: %v", egressNode)
			continue
		}
		klog.V(5).Infof("Egress node: %s about to be initialized", egressNode.Name)
		if err := oc.initEgressIPAllocator(egressNode); err != nil {
			klog.Errorf("Egress node initialization error: %v", err)
		}
	}
}

func (oc *Controller) initEgressIPAllocator(node *kapi.Node) error {
	oc.eIPAllocatorMutex.Lock()
	defer oc.eIPAllocatorMutex.Unlock()
	if _, exists := oc.eIPAllocator[node.Name]; !exists {
		var v4Subnet, v6Subnet *net.IPNet
		var err error
		v4IPNet, v6IPNet, hasAnyIPNet := util.ParseNodePrimaryIP(node)
		if !hasAnyIPNet {
			return fmt.Errorf("node: %q does not have any IP information, unable to use for egress", node.Name)
		}
		if v4IPNet != "" {
			_, v4Subnet, err = net.ParseCIDR(v4IPNet)
			if err != nil {
				return err
			}
		}
		if v6IPNet != "" {
			_, v6Subnet, err = net.ParseCIDR(v6IPNet)
			if err != nil {
				return err
			}
		}
		oc.eIPAllocator[node.Name] = &eNode{
			name:        node.Name,
			v4Subnet:    v4Subnet,
			v6Subnet:    v6Subnet,
			allocations: make(map[string]bool),
		}
	}
	return nil
}
