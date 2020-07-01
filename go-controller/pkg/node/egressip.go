// +build linux

package node

import (
	"fmt"
	"strings"
	"time"

	egressipv1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/egressip/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/vishvananda/netlink"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

const egressLabel = "egress"

func (n *OvnNode) WatchEgressIP() error {
	_, err := n.watchFactory.AddEgressIPHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eIP := obj.(*egressipv1.EgressIP)
			if err := n.addEgressIP(eIP); err != nil {
				klog.Error(err)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldEIP := old.(*egressipv1.EgressIP)
			newEIP := new.(*egressipv1.EgressIP)
			if err := n.deleteEgressIP(oldEIP); err != nil {
				klog.Error(err)
			}
			if err := n.addEgressIP(newEIP); err != nil {
				klog.Error(err)
			}
		},
		DeleteFunc: func(obj interface{}) {
			eIP := obj.(*egressipv1.EgressIP)
			if err := n.deleteEgressIP(eIP); err != nil {
				klog.Error(err)
			}
		},
	}, n.syncEgressIPs)
	return err
}

func (n *OvnNode) isNodeIP(link netlink.Link, addr *netlink.Addr) (bool, error) {
	nodeAddrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return true, err
	}
	for _, nodeAddr := range nodeAddrs {
		if nodeAddr.Label != getEgressLabel(n.defaultGatewayIntf) {
			if addr.IP.Equal(nodeAddr.IP) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (n *OvnNode) addHandler(link netlink.Link, egressIP string) error {
	egressAddr, err := netlink.ParseAddr(fmt.Sprintf("%s/32", egressIP))
	if err != nil {
		return fmt.Errorf("unable to parse EgressIP: %s, err: %v", egressIP, err)
	}
	isNodeIP, err := n.isNodeIP(link, egressAddr)
	if err != nil {
		return fmt.Errorf("unable to list node's IPs")
	}
	if isNodeIP {
		return fmt.Errorf("unable to add own node's IP to interface")
	}
	egressAddr.Label = n.defaultGatewayIntf + egressLabel
	return netlink.AddrReplace(link, egressAddr)
}

func (n *OvnNode) delHandler(link netlink.Link, egressIP string) error {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if strings.Compare(addr.IP.String(), egressIP) == 0 {
			if addr.Label == getEgressLabel(n.defaultGatewayIntf) {
				return netlink.AddrDel(link, &addr)
			}
		}
	}
	return nil
}

func (n *OvnNode) addEgressIP(eIP *egressipv1.EgressIP) error {
	for _, status := range eIP.Status {
		if status.Node == n.name {
			if err := n.handleEgressIPLink(status.EgressIP, n.addHandler); err != nil {
				return fmt.Errorf("unable to add EgressIP to primary interface, err: %v", err)
			}
			go n.notifyClusterNodes(status.EgressIP)
			if err := n.modeEgressIP.setupEgressIP(status); err != nil {
				return fmt.Errorf("unable to setup EgressIP, err: %v ", err)
			}
		}
	}
	return nil
}

func (n *OvnNode) deleteEgressIP(eIP *egressipv1.EgressIP) error {
	for _, status := range eIP.Status {
		if status.Node == n.name {
			if err := n.handleEgressIPLink(status.EgressIP, n.delHandler); err != nil {
				return fmt.Errorf("unable to delete EgressIP from primary interface, err: %v", err)
			}
			if err := n.modeEgressIP.cleanupEgressIP(status); err != nil {
				return fmt.Errorf("unable to delete EgressIP, err: %v ", err)
			}
		}
	}
	return nil
}

func (n *OvnNode) syncEgressIPs(eIPs []interface{}) {
	validEgressIPs := make(map[string]bool)
	for _, eIP := range eIPs {
		eIP, ok := eIP.(*egressipv1.EgressIP)
		if !ok {
			klog.Errorf("Spurious object in syncEgressIPs: %v", eIP)
			continue
		}
		for _, status := range eIP.Status {
			if status.Node == n.name {
				validEgressIPs[status.EgressIP] = true
			}
		}
	}
	primaryLink, err := netlink.LinkByName(n.defaultGatewayIntf)
	if err != nil {
		klog.Errorf("Unable to get link for primary interface: %s, err: %v", n.defaultGatewayIntf, err)
		return
	}
	nodeAddr, err := netlink.AddrList(primaryLink, netlink.FAMILY_ALL)
	if err != nil {
		klog.Errorf("Unable to list addresses on primary interface: %s, err: %v", n.defaultGatewayIntf, err)
		return
	}
	for _, addr := range nodeAddr {
		if addr.Label == getEgressLabel(n.defaultGatewayIntf) {
			if _, exists := validEgressIPs[addr.IP.String()]; !exists {
				if err := netlink.AddrDel(primaryLink, &addr); err != nil {
					klog.Errorf("Unable to delete stale egress IP: %s, err: %v", addr.IP.String(), err)
				}
			}
		}
	}
}

// Notify cluster nodes of change in case an egress IP is moved from one node to another
func (n *OvnNode) notifyClusterNodes(egressIP string) {
	primaryLink, err := netlink.LinkByName(n.defaultGatewayIntf)
	if err != nil {
		klog.Errorf("Unable to get link for primary interface: %s, err: %v", n.defaultGatewayIntf, err)
		return
	}
	_, stderr, err := util.RunArping("-q", "-A", "-c", "1", "-I", primaryLink.Attrs().Name, egressIP)
	if err != nil {
		klog.Warningf("Failed to send ARP claim for egress IP %q: %v (%s)", egressIP, err, string(stderr))
		return
	}
	time.Sleep(2 * time.Second)
	if _, stderr, err := util.RunArping("-q", "-U", "-c", "1", "-I", primaryLink.Attrs().Name, egressIP); err != nil {
		klog.Warningf("Failed to ARP for egress IP %q: %v (%s)", egressIP, err, string(stderr))
	}
}

func (n *OvnNode) handleEgressIPLink(egressIP string, handler func(link netlink.Link, ip string) error) error {
	primaryLink, err := netlink.LinkByName(n.defaultGatewayIntf)
	if err != nil {
		return fmt.Errorf("unable to get link for primary interface: %s, err: %v", n.defaultGatewayIntf, err)
	}
	if err := handler(primaryLink, egressIP); err != nil {
		return fmt.Errorf("unable to handle egressIP on primary interface, err: %v", err)
	}
	return nil
}

func getEgressLabel(defaultGatewayIntf string) string {
	return defaultGatewayIntf + egressLabel
}
