package types

const (
	K8sPrefix = "k8s-"
	// K8sMgmtIntfName name to be used as an OVS internal port on the node
	K8sMgmtIntfName = "ovn-k8s-mp0"

	// PhysicalNetworkName is the name that maps to an OVS bridge that provides
	// access to physical/external network
	PhysicalNetworkName = "physnet"

	// LocalNetworkName is the name that maps to an OVS bridge that provides
	// access to local service
	LocalNetworkName = "locnet"

	// Local Bridge used for DGP access
	LocalBridgeName = "br-local"

	// types.OVNClusterRouter is the name of the distributed router
	OVNClusterRouter = "ovn_cluster_router"
	OVNJoinSwitch    = "join"

	JoinSwitchPrefix             = "join_"
	ExternalSwitchPrefix         = "ext_"
	GWRouterPrefix               = "GR_"
	RouterToSwitchPrefix         = "rtos-"
	InterPrefix                  = "inter-"
	HybridSubnetPrefix           = "hybrid-subnet-"
	SwitchToRouterPrefix         = "stor-"
	JoinSwitchToGWRouterPrefix   = "jtor-"
	GWRouterToJoinSwitchPrefix   = "rtoj-"
	DistRouterToJoinSwitchPrefix = "dtoj-"
	JoinSwitchToDistRouterPrefix = "jtod-"
	EXTSwitchToGWRouterPrefix    = "etor-"
	GWRouterToExtSwitchPrefix    = "rtoe-"

	NodeLocalSwitch = "node_local_switch"

	// ACL directions
	DirectionToLPort   = "to-lport"
	DirectionFromLPort = "from-lport"

	// ACL Priorities

	// Default routed multicast allow acl rule priority
	DefaultRoutedMcastAllowPriority = "10013"
	// Default multicast allow acl rule priority
	DefaultMcastAllowPriority = "10012"
	// Default multicast deny acl rule priority
	DefaultMcastDenyPriority = "10011"
	// Default allow acl rule priority
	DefaultAllowPriority = "10001"
	// Default deny acl rule priority
	DefaultDenyPriority = "10000"
	// Default egress firewall allow priority for internal cluster traffic.
	// Must be below DefaultDenyPriority, since that is used for in-cluster
	// reject ACLs, which takes precedence
	EgressFirewallDefaultInternalClusterPriority = "9999"
	// Default egress firewall priority for egress traffic.
	// Must be below EgressFirewallDefaultInternalClusterPriority, since
	// that is used to allow in-cluster traffic for pods matching an
	// egress firewall rule
	EgressFirewallStartPriority           = "9998"
	MinimumReservedEgressFirewallPriority = "2000"

	// priority of logical router policies on the OVNClusterRouter
	MGMTPortPolicyPriority       = "1005"
	NodeSubnetPolicyPriority     = "1004"
	InterNodePolicyPriority      = "1003"
	HybridOverlaySubnetPriority  = "1002"
	HybridOverlayReroutePriority = "501"
	DefaultNoRereoutePriority    = "101"
	EgressIPReroutePriority      = "100"

	V6NodeLocalNATSubnet           = "fd99::/64"
	V6NodeLocalNATSubnetPrefix     = 64
	V6NodeLocalNATSubnetNextHop    = "fd99::1"
	V6NodeLocalDistributedGWPortIP = "fd99::2"

	V4NodeLocalNATSubnet           = "169.254.0.0/20"
	V4NodeLocalNATSubnetPrefix     = 20
	V4NodeLocalNATSubnetNextHop    = "169.254.0.1"
	V4NodeLocalDistributedGWPortIP = "169.254.0.2"

	V4MasqueradeSubnet = "169.254.169.0/30"
	V4HostMasqueradeIP = "169.254.169.2"
	V6HostMasqueradeIP = "fd69::2"
	V4OVNMasqueradeIP  = "169.254.169.1"
	V6OVNMasqueradeIP  = "fd69::1"

	// OpenFlow and Networking constants
	RouteAdvertisementICMPType    = 134
	NeighborAdvertisementICMPType = 136

	OvnACLLoggingMeter = "acl-logging"

	// LoadBalancer External Names
	ClusterLBTCP          = "k8s-cluster-lb-tcp"
	ClusterLBUDP          = "k8s-cluster-lb-udp"
	ClusterLBSCTP         = "k8s-cluster-lb-sctp"
	ClusterLBPrefix       = "k8s-cluster-lb"
	ClusterIdlingLBPrefix = "k8s-idling-lb"
	WorkerLBPrefix        = "k8s-worker-lb"
	WorkerLBTCP           = WorkerLBPrefix + "-tcp"
	WorkerLBUDP           = WorkerLBPrefix + "-udp"
	WorkerLBSCTP          = WorkerLBPrefix + "-sctp"
	GatewayLBTCP          = "TCP_lb_gateway_router"
	GatewayLBUDP          = "UDP_lb_gateway_router"
	GatewayLBSCTP         = "SCTP_lb_gateway_router"

	// OVN-K8S Topology Versions
	OvnSingleJoinSwitchTopoVersion = 1
	OvnNamespacedDenyPGTopoVersion = 2
	OvnHostToSvcOFTopoVersion      = 3
	OvnPortBindingTopoVersion      = 4
	OvnCurrentTopologyVersion      = OvnPortBindingTopoVersion

	// OVN-K8S annotation constants
	OvnK8sPrefix   = "k8s.ovn.org"
	OvnK8sTopoAnno = OvnK8sPrefix + "/" + "topology-version"

	// Monitoring constants
	SFlowAgent = "ovn-k8s-mp0"

	// OVNKube-Node Node types
	NodeModeFull         = "full"
	NodeModeSmartNIC     = "smart-nic"
	NodeModeSmartNICHost = "smart-nic-host"
)
