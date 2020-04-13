// +build linux

package node

import (
	"fmt"
	"net"
	"reflect"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"k8s.io/klog"

	kapi "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	utilnet "k8s.io/utils/net"
)

const (
	v4localnetGatewayIP           = "169.254.33.2"
	v4localnetGatewayNextHop      = "169.254.33.1"
	v4localnetGatewaySubnetPrefix = 24

	v6localnetGatewayIP           = "fd99::2"
	v6localnetGatewayNextHop      = "fd99::1"
	v6localnetGatewaySubnetPrefix = 64

	// localnetGatewayNextHopPort is the name of the gateway port on the host to which all
	// the packets leaving the OVN logical topology will be forwarded
	localnetGatewayNextHopPort       = "ovn-k8s-gw0"
	legacyLocalnetGatewayNextHopPort = "br-nexthop"
	// fixed MAC address for the br-nexthop interface. the last 4 hex bytes
	// translates to the br-nexthop's IP address
	localnetGatewayNextHopMac = "00:00:a9:fe:21:01"
	iptableNodePortChain      = "OVN-KUBE-NODEPORT"
	iptableExternalIPChain    = "OVN-KUBE-EXTERNALIP"
	// Routing table for ExternalIP communication
	localnetGatwayExternalIDTable = "6"
)

type iptRule struct {
	table string
	chain string
	args  []string
}

func ensureChain(ipt util.IPTablesHelper, table, chain string) error {
	chains, err := ipt.ListChains(table)
	if err != nil {
		return fmt.Errorf("failed to list iptables chains: %v", err)
	}
	for _, ch := range chains {
		if ch == chain {
			return nil
		}
	}

	return ipt.NewChain(table, chain)
}

func addIptRules(ipt util.IPTablesHelper, rules []iptRule) error {
	for _, r := range rules {
		if err := ensureChain(ipt, r.table, r.chain); err != nil {
			return fmt.Errorf("failed to ensure %s/%s: %v", r.table, r.chain, err)
		}
		exists, err := ipt.Exists(r.table, r.chain, r.args...)
		if !exists && err == nil {
			err = ipt.Insert(r.table, r.chain, 1, r.args...)
		}
		if err != nil {
			return fmt.Errorf("failed to add iptables %s/%s rule %q: %v",
				r.table, r.chain, strings.Join(r.args, " "), err)
		}
	}

	return nil
}

func delIptRules(ipt util.IPTablesHelper, rules []iptRule) error {
	for _, r := range rules {
		err := ipt.Delete(r.table, r.chain, r.args...)
		if err != nil {
			return fmt.Errorf("failed to delete iptables %s/%s rule %q: %v", r.table, r.chain,
				strings.Join(r.args, " "), err)
		}
	}
	return nil
}

func generateGatewayNATRules(ifname string, ip net.IP) []iptRule {
	// Allow packets to/from the gateway interface in case defaults deny
	rules := make([]iptRule, 0)
	rules = append(rules, iptRule{
		table: "filter",
		chain: "FORWARD",
		args:  []string{"-i", ifname, "-j", "ACCEPT"},
	})
	rules = append(rules, iptRule{
		table: "filter",
		chain: "FORWARD",
		args: []string{"-o", ifname, "-m", "conntrack", "--ctstate",
			"RELATED,ESTABLISHED", "-j", "ACCEPT"},
	})
	rules = append(rules, iptRule{
		table: "filter",
		chain: "INPUT",
		args:  []string{"-i", ifname, "-m", "comment", "--comment", "from OVN to localhost", "-j", "ACCEPT"},
	})

	// NAT for the interface
	rules = append(rules, iptRule{
		table: "nat",
		chain: "POSTROUTING",
		args:  []string{"-s", ip.String(), "-j", "MASQUERADE"},
	})
	return rules
}

func localnetGatewayNAT(ipt util.IPTablesHelper, ifname string, ip net.IP) error {
	rules := generateGatewayNATRules(ifname, ip)
	return addIptRules(ipt, rules)
}

func (n *OvnNode) initLocalnetGateway(subnet *net.IPNet, nodeAnnotator kube.Annotator) error {
	// Create a localnet OVS bridge.
	localnetBridgeName := "br-local"
	_, stderr, err := util.RunOVSVsctl("--may-exist", "add-br",
		localnetBridgeName)
	if err != nil {
		return fmt.Errorf("Failed to create localnet bridge %s"+
			", stderr:%s (%v)", localnetBridgeName, stderr, err)
	}

	ifaceID, macAddress, err := bridgedGatewayNodeSetup(n.name, localnetBridgeName, localnetBridgeName, true)
	if err != nil {
		return fmt.Errorf("failed to set up shared interface gateway: %v", err)
	}
	_, err = util.LinkSetUp(localnetBridgeName)
	if err != nil {
		return err
	}

	// Create a localnet bridge gateway port
	_, stderr, err = util.RunOVSVsctl(
		"--if-exists", "del-port", localnetBridgeName, legacyLocalnetGatewayNextHopPort,
		"--", "--may-exist", "add-port", localnetBridgeName, localnetGatewayNextHopPort,
		"--", "set", "interface", localnetGatewayNextHopPort, "type=internal",
		"mtu_request="+fmt.Sprintf("%d", config.Default.MTU),
		fmt.Sprintf("mac=%s", strings.ReplaceAll(localnetGatewayNextHopMac, ":", "\\:")))
	if err != nil {
		return fmt.Errorf("Failed to create localnet bridge gateway port %s"+
			", stderr:%s (%v)", localnetGatewayNextHopPort, stderr, err)
	}
	link, err := util.LinkSetUp(localnetGatewayNextHopPort)
	if err != nil {
		return err
	}

	var gatewayIP, gatewayNextHop net.IP
	var gatewaySubnetMask net.IPMask
	if utilnet.IsIPv6CIDR(subnet) {
		gatewayIP = net.ParseIP(v6localnetGatewayIP)
		gatewayNextHop = net.ParseIP(v6localnetGatewayNextHop)
		gatewaySubnetMask = net.CIDRMask(v6localnetGatewaySubnetPrefix, 128)
	} else {
		gatewayIP = net.ParseIP(v4localnetGatewayIP)
		gatewayNextHop = net.ParseIP(v4localnetGatewayNextHop)
		gatewaySubnetMask = net.CIDRMask(v4localnetGatewaySubnetPrefix, 32)
	}
	gatewayIPCIDR := &net.IPNet{IP: gatewayIP, Mask: gatewaySubnetMask}
	gatewayNextHopCIDR := &net.IPNet{IP: gatewayNextHop, Mask: gatewaySubnetMask}

	// Flush any addresses on localnetBridgeNextHopPort and add the new IP address.
	if err = util.LinkAddrFlush(link); err == nil {
		err = util.LinkAddrAdd(link, gatewayNextHopCIDR)
	}
	if err != nil {
		return err
	}

	err = util.SetLocalL3GatewayConfig(nodeAnnotator, ifaceID, macAddress,
		gatewayIPCIDR, gatewayNextHop, config.Gateway.NodeportEnable)
	if err != nil {
		return err
	}

	if utilnet.IsIPv6CIDR(subnet) {
		// TODO - IPv6 hack ... for some reason neighbor discovery isn't working here, so hard code a
		// MAC binding for the gateway IP address for now - need to debug this further

		err = util.LinkNeighAdd(link, gatewayIP, macAddress)
		if err == nil {
			klog.Infof("Added MAC binding for %s on %s", gatewayIP, localnetGatewayNextHopPort)
		} else {
			klog.Errorf("Error in adding MAC binding for %s on %s: %v", gatewayIP, localnetGatewayNextHopPort, err)
		}
	}

	ipt, err := localnetIPTablesHelper(subnet)
	if err != nil {
		return err
	}

	err = localnetGatewayNAT(ipt, localnetGatewayNextHopPort, gatewayIP)
	if err != nil {
		return fmt.Errorf("Failed to add NAT rules for localnet gateway (%v)", err)
	}

	if config.Gateway.NodeportEnable {
		err = n.localnetNodePortWatcher(
			newLocalnetNodePortWatcherData(ipt, gatewayIP, n.recorder),
		)
	}

	return err
}

// localnetIPTablesHelper gets an IPTablesHelper for IPv4 or IPv6 as appropriate
func localnetIPTablesHelper(subnet *net.IPNet) (util.IPTablesHelper, error) {
	var ipt util.IPTablesHelper
	var err error
	if utilnet.IsIPv6CIDR(subnet) {
		ipt, err = util.GetIPTablesHelper(iptables.ProtocolIPv6)
	} else {
		ipt, err = util.GetIPTablesHelper(iptables.ProtocolIPv4)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize iptables: %v", err)
	}
	return ipt, nil
}

type activeSocket interface {
	Close() error
}

type localNodePortWatcher interface {
	initIPTables() error
	getLocalAddrs() error
	addService(svc *kapi.Service) error
	deleteService(svc *kapi.Service) error
	syncServices(serviceInterface []interface{})
}

type localnetNodePortWatcherData struct {
	localNodePortWatcher
	recorder      record.EventRecorder
	ipt           util.IPTablesHelper
	gatewayIP     string
	localAddrSet  map[string]net.IPNet
	activeSockets map[int32]activeSocket
}

func newLocalnetNodePortWatcherData(ipt util.IPTablesHelper, gatewayIP net.IP, recorder record.EventRecorder) *localnetNodePortWatcherData {
	return &localnetNodePortWatcherData{
		ipt:           ipt,
		gatewayIP:     gatewayIP.String(),
		recorder:      recorder,
		activeSockets: make(map[int32]activeSocket),
	}
}

func (npw *localnetNodePortWatcherData) openLocalPort(port int32, protocol kapi.Protocol, svc *kapi.Service) error {
	var socket activeSocket
	var socketError error
	switch protocol {
	case kapi.ProtocolTCP:
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			socketError = err
			break
		}
		socket = listener
		break
	case kapi.ProtocolUDP:
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			socketError = err
			break
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			socketError = err
			break
		}
		socket = conn
		break
	default:
		socketError = fmt.Errorf("unknown protocol %q", protocol)
	}
	if socketError != nil {
		npw.emitPortClaimEvent(svc, port, socketError)
		return socketError
	}
	npw.activeSockets[port] = socket
	return nil
}

func (npw *localnetNodePortWatcherData) getLocalAddrs() error {
	npw.localAddrSet = make(map[string]net.IPNet)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		ip, ipNet, err := net.ParseCIDR(addr.String())
		if err != nil {
			return err
		}
		npw.localAddrSet[ip.String()] = *ipNet
	}
	klog.V(5).Infof("node local addresses initialized to: %v", npw.localAddrSet)
	return nil
}

func (npw *localnetNodePortWatcherData) nodePortIPTRules(svcPort v1.ServicePort, gatewayIP string) []iptRule {
	return []iptRule{
		iptRule{
			table: "nat",
			chain: iptableNodePortChain,
			args: []string{
				"-p", string(svcPort.Protocol),
				"--dport", fmt.Sprintf("%d", svcPort.NodePort),
				"-j", "DNAT",
				"--to-destination", net.JoinHostPort(gatewayIP, fmt.Sprintf("%d", svcPort.NodePort)),
			},
		},
		iptRule{
			table: "filter",
			chain: iptableNodePortChain,
			args: []string{
				"-p", string(svcPort.Protocol),
				"--dport", fmt.Sprintf("%d", svcPort.NodePort),
				"-j", "ACCEPT",
			},
		},
	}
}

func (npw *localnetNodePortWatcherData) externalIPTRules(svcPort v1.ServicePort, externalIP, dstIP string) []iptRule {
	return []iptRule{
		iptRule{
			table: "nat",
			chain: iptableExternalIPChain,
			args: []string{
				"-p", string(svcPort.Protocol),
				"-d", externalIP,
				"--dport", fmt.Sprintf("%v", svcPort.Port),
				"-j", "DNAT",
				"--to-destination", net.JoinHostPort(dstIP, fmt.Sprintf("%v", svcPort.Port)),
			},
		},
		iptRule{
			table: "filter",
			chain: iptableExternalIPChain,
			args: []string{
				"-p", string(svcPort.Protocol),
				"-d", externalIP,
				"--dport", fmt.Sprintf("%v", svcPort.Port),
				"-j", "ACCEPT",
			},
		},
	}
}

func (npw *localnetNodePortWatcherData) isNetworkConnected(ipNet net.IP) bool {
	for _, net := range npw.localAddrSet {
		if net.Contains(ipNet) {
			return true
		}
	}
	return false
}

func (npw *localnetNodePortWatcherData) emitPortClaimEvent(svc *kapi.Service, port int32, err error) {
	serviceRef := kapi.ObjectReference{
		Kind:      "Service",
		Namespace: svc.Namespace,
		Name:      svc.Name,
	}
	npw.recorder.Eventf(&serviceRef, kapi.EventTypeWarning, "PortClaim", "Service: %s specifies node local port: %v, but port cannot be bound to: %v", svc.Name, port, err)
	klog.Warningf("PortClaim for svc: %s on port: %v, err: %v", svc.Name, port, err)
}

// Cases to consider here:
// 1) We only care about valid services of type NodePort or services with ExternalIPs assigned - these are mutually exclusive
// 2) We try to do as much as possible (i.e: don't fail as soon as an error is encountered)
// 3) Three cases of ExternalIPs can be defined
//	3.1) An ExternalIP that is assigned to any interface on the node
//	3.2) An ExternalIP that is within the same subnetwork as any interface on the node
//	3.3) An ExternalIP that is external to the node's network (i.e: someting can route to this node, but we don't necessarily know what that is - the user takes care of that)
// 4) Case 3.1) and NodePort services predicates that the port that the services defines is not already in use by another process on the host or service does not try to bind to special ports (80, 443, etc)
// 5) Case 3.1) predicates a defined ClusterIP to DNAT to. Otherwise we can't get it into OVN.
// 6) If 5) is not fulfilled: notify the user of this unsupported feature, by emitting an event
// 7) If 4) is not fulfilled: notify the user of this, by emitting an event
func (npw *localnetNodePortWatcherData) addService(svc *kapi.Service) error {
	iptRules := []iptRule{}
	for _, port := range svc.Spec.Ports {
		if util.ServiceTypeHasNodePort(svc) {
			if err := util.ValidatePort(port.Protocol, port.NodePort); err != nil {
				klog.Warningf("invalid service node port %s, err: %v", port.Name, err)
				continue
			}
			if err := npw.openLocalPort(port.NodePort, port.Protocol, svc); err != nil {
				continue
			}
			iptRules = append(iptRules, npw.nodePortIPTRules(port, npw.gatewayIP)...)
		}
		for _, externalIP := range svc.Spec.ExternalIPs {
			if err := util.ValidatePort(port.Protocol, port.Port); err != nil {
				klog.Warningf("invalid service port %s, err: %v", port.Name, err)
				continue
			}
			if _, exists := npw.localAddrSet[externalIP]; exists {
				if !util.IsClusterIPSet(svc) {
					serviceRef := kapi.ObjectReference{
						Kind:      "Service",
						Namespace: svc.Namespace,
						Name:      svc.Name,
					}
					npw.recorder.Eventf(&serviceRef, kapi.EventTypeWarning, "UnsupportedServiceDefinition", "Unsupported service definition, headless service: %s with a local ExternalIP is not supported by ovn-kubernetes in local gateway mode", svc.Name)
					klog.Warningf("UnsupportedServiceDefinition event for service %s in namespace %s", svc.Name, svc.Namespace)
					continue
				}
				if err := npw.openLocalPort(port.Port, port.Protocol, svc); err != nil {
					continue
				}
				iptRules = append(iptRules, npw.externalIPTRules(port, externalIP, svc.Spec.ClusterIP)...)
				continue
			}
			if npw.isNetworkConnected(net.ParseIP(externalIP)) {
				continue
			}
			if stdout, stderr, err := util.RunIP("route", "add", externalIP, "via", npw.gatewayIP, "dev", localnetGatewayNextHopPort, "table", localnetGatwayExternalIDTable); err != nil {
				klog.Errorf("error adding routing table entry for ExternalIP %s: stdout: %s, stderr: %s, err: %v", externalIP, stdout, stderr, err)
			}
		}
	}
	klog.V(5).Infof("add iptables rules: %v for service: %v", iptRules, svc.Name)
	return addIptRules(npw.ipt, iptRules)
}

// Cases to consider here:
// 1) The effects of 4) from addService means that we only need to look at activeSockets when deleting iptables rules.
func (npw *localnetNodePortWatcherData) deleteService(svc *kapi.Service) error {
	iptRules := []iptRule{}
	for _, port := range svc.Spec.Ports {
		if util.ServiceTypeHasNodePort(svc) {
			if socket, exists := npw.activeSockets[port.NodePort]; exists {
				if err := socket.Close(); err != nil {
					klog.Errorf("error closing socket for NodePort svc: %s on port: %v, err: %v", svc.Name, port, err)
				}
				delete(npw.activeSockets, port.NodePort)
				iptRules = append(iptRules, npw.nodePortIPTRules(port, npw.gatewayIP)...)
			}
		}
		for _, externalIP := range svc.Spec.ExternalIPs {
			if _, exists := npw.localAddrSet[externalIP]; exists {
				if socket, exists := npw.activeSockets[port.Port]; exists {
					if err := socket.Close(); err != nil {
						klog.Errorf("error closing socket for ExternalIP svc: %s on port: %v, err: %v", svc.Name, port, err)
					}
					delete(npw.activeSockets, port.Port)
					iptRules = append(iptRules, npw.externalIPTRules(port, externalIP, svc.Spec.ClusterIP)...)
					continue
				}
			}
			if npw.isNetworkConnected(net.ParseIP(externalIP)) {
				continue
			}
			if stdout, stderr, err := util.RunIP("route", "del", externalIP, "via", npw.gatewayIP, "dev", localnetGatewayNextHopPort, "table", localnetGatwayExternalIDTable); err != nil {
				klog.Errorf("error delete routing table entry for ExternalIP %s: stdout: %s, stderr: %s, err: %v", externalIP, stdout, stderr, err)
			}
		}
	}
	klog.V(5).Infof("delete iptables rules: %v for service: %v", iptRules, svc.Name)
	return delIptRules(npw.ipt, iptRules)
}

func (npw *localnetNodePortWatcherData) syncServices(serviceInterface []interface{}) {
	removeStaleIPTRules := func(table, chain string, serviceRules []iptRule) {
		if ipTableRules, err := npw.ipt.List(table, chain); err == nil {
			for _, ipTableRule := range ipTableRules {
				isFound := false
				for _, serviceRule := range serviceRules {
					if ipTableRule == strings.Join(serviceRule.args, " ") {
						isFound = true
						break
					}
				}
				if !isFound {
					klog.V(5).Infof("deleting stale iptables rule: table: %s, chain: %s, rule: %s", table, chain, ipTableRule)
					if err := npw.ipt.Delete(table, chain, strings.Fields(ipTableRule)...); err != nil {
						klog.Errorf("error deleting stale iptables rule: table: %s, chain: %s, rule: %v", table, chain, ipTableRule)
					}
				}
			}
		}
	}
	removeStaleRoutes := func(externalIPs []string) {
		stdout, stderr, err := util.RunIP("route", "list", "table", localnetGatwayExternalIDTable)
		if err != nil || stdout == "" {
			klog.Infof("no routing table entries for ExternalIP table %s: stdout: %s, stderr: %s, err: %v", localnetGatwayExternalIDTable, stdout, stderr, err)
			return
		}
		for _, existingRoute := range strings.Split(stdout, "\n") {
			isFound := false
			for _, externalIP := range externalIPs {
				if strings.Contains(existingRoute, externalIP) {
					isFound = true
					break
				}
			}
			if !isFound {
				klog.V(5).Infof("deleting stale routing rule: %s", existingRoute)
				if _, stderr, err := util.RunIP("route", "del", existingRoute, "table", localnetGatwayExternalIDTable); err != nil {
					klog.Errorf("error deleting stale routing rule: stderr: %s, err: %v", stderr, err)
				}
			}
		}
	}

	// Here it's actually in our favour to skip the conditions listed in addService
	// We won't be adding any rules from this point on,
	// so try to increase the probability of deleting anything that might have gone wrong before.
	serviceRules := []iptRule{}
	exteriorExternalIPs := []string{}
	for _, service := range serviceInterface {
		svc, ok := service.(*kapi.Service)
		if !ok {
			klog.Errorf("Spurious object in syncServices: %v", serviceInterface)
			continue
		}
		for _, port := range svc.Spec.Ports {
			if util.ServiceTypeHasNodePort(svc) {
				serviceRules = append(serviceRules, npw.nodePortIPTRules(port, npw.gatewayIP)...)
			}
			for _, externalIP := range svc.Spec.ExternalIPs {
				serviceRules = append(serviceRules, npw.externalIPTRules(port, externalIP, svc.Spec.ClusterIP)...)
				exteriorExternalIPs = append(exteriorExternalIPs, externalIP)
			}
		}
	}
	for _, chain := range []string{iptableNodePortChain, iptableExternalIPChain} {
		removeStaleIPTRules("nat", chain, serviceRules)
		removeStaleIPTRules("filter", chain, serviceRules)
	}
	removeStaleRoutes(exteriorExternalIPs)
}

func (npw *localnetNodePortWatcherData) initIPTables() error {
	rules := make([]iptRule, 0)
	for _, chain := range []string{iptableNodePortChain, iptableExternalIPChain} {
		npw.ipt.NewChain("nat", chain)
		npw.ipt.NewChain("filter", chain)
		rules = append(rules, iptRule{
			table: "nat",
			chain: "PREROUTING",
			args:  []string{"-j", chain},
		})
		rules = append(rules, iptRule{
			table: "nat",
			chain: "OUTPUT",
			args:  []string{"-j", chain},
		})
		rules = append(rules, iptRule{
			table: "filter",
			chain: "FORWARD",
			args:  []string{"-j", chain},
		})
	}
	if err := addIptRules(npw.ipt, rules); err != nil {
		return err
	}
	return nil
}

func initRoutingRules() error {
	stdout, stderr, err := util.RunIP("rule")
	if err != nil {
		return fmt.Errorf("error listing routing rules, stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, fmt.Sprintf("from all lookup %s", localnetGatwayExternalIDTable)) {
		if stdout, stderr, err := util.RunIP("rule", "add", "from", "all", "table", localnetGatwayExternalIDTable); err != nil {
			return fmt.Errorf("error adding routing rule for ExternalIP table (%s): stdout: %s, stderr: %s, err: %v", localnetGatwayExternalIDTable, stdout, stderr, err)
		}
	}
	return nil
}

func (n *OvnNode) localnetNodePortWatcher(npw localNodePortWatcher) error {
	if err := npw.initIPTables(); err != nil {
		return err
	}
	if err := initRoutingRules(); err != nil {
		return err
	}
	if err := npw.getLocalAddrs(); err != nil {
		return fmt.Errorf("error listing node interface addresses: %v", err)
	}
	_, err := n.watchFactory.AddServiceHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*kapi.Service)
			err := npw.addService(svc)
			if err != nil {
				klog.Errorf("Error in adding service: %v", err)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			svcNew := new.(*kapi.Service)
			svcOld := old.(*kapi.Service)
			if reflect.DeepEqual(svcNew.Spec, svcOld.Spec) {
				return
			}
			err := npw.deleteService(svcOld)
			if err != nil {
				klog.Errorf("Error in deleting service - %v", err)
			}
			err = npw.addService(svcNew)
			if err != nil {
				klog.Errorf("Error in modifying service: %v", err)
			}
		},
		DeleteFunc: func(obj interface{}) {
			svc := obj.(*kapi.Service)
			err := npw.deleteService(svc)
			if err != nil {
				klog.Errorf("Error in deleting service - %v", err)
			}
		},
	}, npw.syncServices)
	return err
}

// cleanupLocalnetGateway cleans up Localnet Gateway
func cleanupLocalnetGateway() error {
	// get bridgeName from ovn-bridge-mappings.
	stdout, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".",
		"external_ids:ovn-bridge-mappings")
	if err != nil {
		return fmt.Errorf("Failed to get ovn-bridge-mappings stderr:%s (%v)", stderr, err)
	}
	bridgeName := strings.Split(stdout, ":")[1]
	_, stderr, err = util.RunOVSVsctl("--", "--if-exists", "del-br", bridgeName)
	if err != nil {
		return fmt.Errorf("Failed to ovs-vsctl del-br %s stderr:%s (%v)", bridgeName, stderr, err)
	}
	return err
}
