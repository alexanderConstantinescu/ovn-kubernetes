package ovn

import (
	"fmt"
	"net"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/reference"
	"k8s.io/klog"
)

func (ovn *Controller) syncServices(services []interface{}) {
	// For all clusterIP in k8s, we will populate the below slice with
	// IP:port. In OVN's database those are the keys. We need to
	// have separate slice for TCP, SCTP, and UDP load-balancers (hence the dict).
	clusterServices := make(map[kapi.Protocol][]string)

	// For all nodePorts in k8s, we will populate the below slice with
	// nodePort. In OVN's database, nodeIP:nodePort is the key.
	// We have separate slice for TCP, SCTP, and UDP nodePort load-balancers.
	// We will get nodeIP separately later.
	nodeportServices := make(map[kapi.Protocol][]string)

	// For all externalIPs in k8s, we will populate the below map of slices
	// with loadbalancer type services based on each protocol.
	lbServices := make(map[kapi.Protocol][]string)

	// Go through the k8s services and populate 'clusterServices',
	// 'nodeportServices' and 'lbServices'
	for _, serviceInterface := range services {
		service, ok := serviceInterface.(*kapi.Service)
		if !ok {
			klog.Errorf("Spurious object in syncServices: %v",
				serviceInterface)
			continue
		}

		if !util.ServiceTypeHasClusterIP(service) {
			continue
		}

		for _, svcPort := range service.Spec.Ports {
			if err := util.ValidatePort(svcPort.Protocol, svcPort.Port); err != nil {
				klog.Errorf("Error validating protocol for port %s: %v", svcPort.Name, err)
				continue
			}

			if len(service.Spec.ExternalIPs) == 0 {
				continue
			}
			for _, extIP := range service.Spec.ExternalIPs {
				key := util.JoinHostPortInt32(extIP, svcPort.Port)
				lbServices[svcPort.Protocol] = append(lbServices[svcPort.Protocol], key)

			}

			if !util.IsClusterIPSet(service) {
				klog.V(5).Infof("Skipping service %s due to clusterIP = %q", service.Name, service.Spec.ClusterIP)
				break
			}

			if util.ServiceTypeHasNodePort(service) {
				port := fmt.Sprintf("%d", svcPort.NodePort)
				nodeportServices[svcPort.Protocol] = append(nodeportServices[svcPort.Protocol], port)
			}

			key := util.JoinHostPortInt32(service.Spec.ClusterIP, svcPort.Port)
			clusterServices[svcPort.Protocol] = append(clusterServices[svcPort.Protocol], key)
		}
	}

	// Get OVN's current cluster load-balancer VIPs and delete them if they
	// are stale.
	for _, protocol := range []kapi.Protocol{kapi.ProtocolTCP, kapi.ProtocolUDP, kapi.ProtocolSCTP} {
		loadBalancer, err := ovn.getLoadBalancer(protocol)
		if err != nil {
			klog.Errorf("Failed to get load-balancer for %s (%v)",
				kapi.Protocol(protocol), err)
			continue
		}

		loadBalancerVIPS, err := ovn.getLoadBalancerVIPs(loadBalancer)
		if err != nil {
			klog.Errorf("failed to get load-balancer vips for %s (%v)",
				loadBalancer, err)
			continue
		}
		if loadBalancerVIPS == nil {
			continue
		}

		for vip := range loadBalancerVIPS {
			if !stringSliceMembership(clusterServices[protocol], vip) {
				klog.V(5).Infof("Deleting stale cluster vip %s in "+
					"loadbalancer %s", vip, loadBalancer)
				ovn.deleteLoadBalancerVIP(loadBalancer, vip)
			}
		}
	}

	ovn.forEachGatewayLB([]kapi.Protocol{kapi.ProtocolTCP, kapi.ProtocolUDP, kapi.ProtocolSCTP},
		func(gateway, loadBalancer string, protocol kapi.Protocol) error {
			loadBalancerVIPS, err := ovn.getLoadBalancerVIPs(loadBalancer)
			if err != nil || loadBalancerVIPS == nil {
				klog.Errorf("failed to get load-balancer vips for %s (%v)", loadBalancer, err)
				return nil
			}
			for vip := range loadBalancerVIPS {
				_, port, err := net.SplitHostPort(vip)
				if err != nil {
					// In a OVN load-balancer, we should always have vip:port.
					// In the unlikely event that it is not the case, skip it.
					klog.Errorf("failed to split %s to vip and port (%v)", vip, err)
					continue
				}
				if !stringSliceMembership(nodeportServices[protocol], port) && !stringSliceMembership(lbServices[protocol], vip) {
					klog.V(5).Infof("Deleting stale nodeport vip %s in loadbalancer %s", vip, loadBalancer)
					ovn.deleteLoadBalancerVIP(loadBalancer, vip)
				}
			}
			return nil
		})
}

func (ovn *Controller) createServiceLoadBalancer(loadBalancerIP, loadBalancer string, port int32, protocol kapi.Protocol) error {
	// With the physical_ip:port as the VIP, add an entry in 'load_balancer'.
	vip := util.JoinHostPortInt32(loadBalancerIP, port)
	// Skip creating LB if endpoints watcher already did it
	if _, hasEps := ovn.getServiceLBInfo(loadBalancer, vip); hasEps {
		klog.V(5).Infof("Load Balancer already configured for %s, %s", loadBalancer, vip)
		return nil
	}
	aclUUID, err := ovn.createLoadBalancerRejectACL(loadBalancer, loadBalancerIP, port, protocol)
	if err != nil {
		return fmt.Errorf("failed to create service ACL")
	}
	klog.V(5).Infof("Service Reject ACL created for physical gateway: %s", aclUUID)
	return nil
}

func (ovn *Controller) createService(service *kapi.Service) error {
	klog.V(5).Infof("Creating service %s", service.Name)
	if !util.IsClusterIPSet(service) {
		klog.V(5).Infof("Skipping service create: No cluster IP for service %s found", service.Name)
		return nil
	} else if len(service.Spec.Ports) == 0 {
		klog.V(5).Info("Skipping service create: No Ports specified for service")
		return nil
	}

	for _, svcPort := range service.Spec.Ports {
		var port int32
		if util.ServiceTypeHasNodePort(service) {
			port = svcPort.NodePort
		} else {
			port = svcPort.Port
		}

		if err := util.ValidatePort(svcPort.Protocol, port); err != nil {
			return fmt.Errorf("error validating protocol for port %s: %v", svcPort.Name, err)
		}

		if !ovn.SCTPSupport && svcPort.Protocol == kapi.ProtocolSCTP {
			ref, err := reference.GetReference(scheme.Scheme, service)
			if err != nil {
				klog.Errorf("Could not get reference for pod %v: %v\n", service.Name, err)
			} else {
				ovn.recorder.Event(ref, kapi.EventTypeWarning, "Unsupported protocol error",
					"SCTP protocol is unsupported by this version of OVN")
			}
			return fmt.Errorf("invalid service port %s: SCTP is unsupported by this version of OVN", svcPort.Name)
		}

		if util.ServiceTypeHasNodePort(service) && ovn.svcQualifiesForReject(service) {
			// Each gateway has a separate load-balancer for N/S traffic
			err := ovn.forEachGatewayLB([]kapi.Protocol{svcPort.Protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
				physicalIP, err := ovn.getGatewayPhysicalIP(gateway)
				if err != nil {
					return fmt.Errorf("physical gateway %s does not have physical ip (%v)", gateway, err)
				}
				return ovn.createServiceLoadBalancer(physicalIP, loadBalancer, port, protocol)
			})
			if err != nil {
				return err
			}
		}
		if util.ServiceTypeHasClusterIP(service) && ovn.svcQualifiesForReject(service) {
			loadBalancer, err := ovn.getLoadBalancer(svcPort.Protocol)
			if err != nil {
				return fmt.Errorf("Failed to get load-balancer for %s (%v)", svcPort.Protocol, err)
			}
			if err := ovn.createServiceLoadBalancer(service.Spec.ClusterIP, loadBalancer, port, svcPort.Protocol); err != nil {
				return err
			}
			for _, extIP := range service.Spec.ExternalIPs {
				err := ovn.forEachGatewayLB([]kapi.Protocol{svcPort.Protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
					return ovn.createServiceLoadBalancer(extIP, loadBalancer, port, protocol)
				})
				if err != nil {
					return err
				}
			}

		}
	}
	return nil
}

func (ovn *Controller) updateService(oldSvc, newSvc *kapi.Service) error {
	klog.V(5).Infof("Updating service is a noop: %s", newSvc.Name)
	// Service update needs to check for port change, protocol, etc and update the ACLs, or and OVN LBs
	// This only really matters when a service is updated and has no endpoints. If the service has endpoints
	// the endpoints watcher will handle the OVN config
	// TODO (trozet) implement this
	return nil
}

func (ovn *Controller) deleteService(service *kapi.Service) {
	if !util.IsClusterIPSet(service) || len(service.Spec.Ports) == 0 {
		return
	}
	for _, svcPort := range service.Spec.Ports {
		var port int32
		if util.ServiceTypeHasNodePort(service) {
			port = svcPort.NodePort
		} else {
			port = svcPort.Port
		}

		if err := util.ValidatePort(svcPort.Protocol, port); err != nil {
			klog.Errorf("Skipping delete for service port %s: %v", svcPort.Name, err)
			continue
		}

		if util.ServiceTypeHasNodePort(service) {
			ovn.deleteGatewayNodePortVIPs(svcPort.Protocol, port)
		}
		loadBalancer, err := ovn.getLoadBalancer(svcPort.Protocol)
		if err != nil {
			klog.Errorf("Failed to get load-balancer for %s (%v)", svcPort.Protocol, err)
			break
		}
		vip := util.JoinHostPortInt32(service.Spec.ClusterIP, svcPort.Port)
		ovn.deleteLoadBalancerVIP(loadBalancer, vip)
		ovn.deleteGatewayExternalIPVIPs(service, svcPort)
	}
}

// svcQualifiesForReject determines if a service should have a reject ACL on it when it has no endpoints
// The reject ACL is only applied to terminate incoming connections immediately when idling is not used
// or OVNEmptyLbEvents are not enabled. When idilng or empty LB events are enabled, we want to ensure we
// receive these packets and not reject them.
func (ovn *Controller) svcQualifiesForReject(service *kapi.Service) bool {
	_, ok := service.Annotations[OvnServiceIdledAt]
	return !(config.Kubernetes.OVNEmptyLbEvents && ok)
}
