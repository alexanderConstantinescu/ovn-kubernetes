package ovn

import (
	"fmt"
	"strings"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/klog"
)

func (ovn *Controller) getOvnGateways() ([]string, string, error) {
	// Return all created gateways.
	out, stderr, err := util.RunOVNNbctl("--data=bare", "--no-heading",
		"--columns=name", "find",
		"logical_router",
		"options:chassis!=null")
	return strings.Fields(out), stderr, err
}

func (ovn *Controller) getGatewayPhysicalIP(physicalGateway string) (string, error) {
	physicalIP, _, err := util.RunOVNNbctl("get", "logical_router",
		physicalGateway, "external_ids:physical_ip")
	if err != nil {
		return "", err
	}

	return physicalIP, nil
}

func (ovn *Controller) getGatewayLoadBalancer(physicalGateway string, protocol kapi.Protocol) (string, error) {
	externalIDKey := string(protocol) + "_lb_gateway_router"
	loadBalancer, _, err := util.RunOVNNbctl("--data=bare", "--no-heading",
		"--columns=_uuid", "find", "load_balancer",
		"external_ids:"+externalIDKey+"="+
			physicalGateway)
	if err != nil {
		return "", err
	}
	return loadBalancer, nil
}

func (ovn *Controller) forEachGatewayLB(protocols []kapi.Protocol, doCallback func(string, string, kapi.Protocol) error) error {
	gateways, _, err := ovn.getOvnGateways()
	if err != nil {
		return err
	}
	for _, gateway := range gateways {
		for _, protocol := range protocols {
			loadBalancer, err := ovn.getGatewayLoadBalancer(gateway, protocol)
			if err != nil || loadBalancer == "" {
				return fmt.Errorf("gateway %s does not have load_balancer (%v)", gateway, err)
			}
			if err := doCallback(gateway, loadBalancer, protocol); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ovn *Controller) createGatewayNodePortVIPs(protocol kapi.Protocol, port, targetPort int32, ips []string) error {
	klog.V(5).Infof("Creating Gateway NodePort vip - %s, %d, %d, %v", protocol, port, targetPort, ips)
	return ovn.forEachGatewayLB([]kapi.Protocol{protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
		physicalIP, err := ovn.getGatewayPhysicalIP(gateway)
		if err != nil {
			return fmt.Errorf("physical gateway %s does not have physical ip (%v)", gateway, err)
		}
		return ovn.createLoadBalancerVIP(loadBalancer, physicalIP, port, ips, targetPort)
	})
}

func (ovn *Controller) deleteGatewayNodePortVIPs(protocol kapi.Protocol, port int32) {
	klog.V(5).Infof("Deleting Gateway NodePort vip - %s, %d", protocol, port)
	err := ovn.forEachGatewayLB([]kapi.Protocol{protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
		physicalIP, err := ovn.getGatewayPhysicalIP(gateway)
		if err != nil {
			return fmt.Errorf("physical gateway %s does not have physical ip (%v)", gateway, err)
		}
		vip := util.JoinHostPortInt32(physicalIP, port)
		klog.V(5).Infof("Deleting Gateway NodePort vip: %s from loadbalancer: %s", vip, loadBalancer)
		return ovn.deleteLoadBalancerVIP(loadBalancer, vip)
	})
	if err != nil {
		klog.Error(err)
	}
}

func (ovn *Controller) createGatewayExternalIPVIPs(svc *kapi.Service, svcPort kapi.ServicePort, ips []string, targetPort int32) {
	klog.V(5).Infof("Creating Gateway ExternalIPs vip for svc %v", svc.Name)
	for _, extIP := range svc.Spec.ExternalIPs {
		err := ovn.forEachGatewayLB([]kapi.Protocol{svcPort.Protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
			return ovn.createLoadBalancerVIP(loadBalancer, extIP, svcPort.Port, ips, targetPort)
		})
		if err != nil {
			klog.Error(err)
		}
	}
}

func (ovn *Controller) deleteGatewayExternalIPVIPs(svc *kapi.Service, svcPort kapi.ServicePort) {
	klog.V(5).Infof("Deleting Gateway ExternalIPs vip for svc %v", svc.Name)
	for _, extIP := range svc.Spec.ExternalIPs {
		err := ovn.forEachGatewayLB([]kapi.Protocol{svcPort.Protocol}, func(gateway, loadBalancer string, protocol kapi.Protocol) error {
			vip := util.JoinHostPortInt32(extIP, svcPort.Port)
			klog.V(5).Infof("Deleting Gateway ExternalIPs vip: %s from load balancer: %s", vip, loadBalancer)
			return ovn.deleteLoadBalancerVIP(loadBalancer, vip)
		})
		if err != nil {
			klog.Error(err)
		}
	}
}
