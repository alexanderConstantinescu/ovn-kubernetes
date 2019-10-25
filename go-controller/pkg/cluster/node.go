package cluster

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
)

func isOVNControllerReady(name string) (bool, error) {
	const runDir string = "/var/run/openvswitch/"

	pid, err := ioutil.ReadFile(runDir + "ovn-controller.pid")
	if err != nil {
		return false, fmt.Errorf("unknown pid for ovn-controller process: %v", err)
	}

	err = wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
		ctlFile := runDir + fmt.Sprintf("ovn-controller.%s.ctl", strings.TrimSuffix(string(pid), "\n"))
		ret, _, err := util.RunOVSAppctl("-t", ctlFile, "connection-status")
		if err == nil {
			logrus.Infof("node %s connection status = %s", name, ret)
			return ret == "connected", nil
		}
		return false, err
	})
	if err != nil {
		return false, fmt.Errorf("timed out waiting sbdb for node %s: %v", name, err)
	}

	err = wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
		flows, _, err := util.RunOVSOfctl("dump-flows", "br-int")
		return len(flows) > 0, err
	})
	if err != nil {
		return false, fmt.Errorf("timed out dumping br-int flow entries for node %s: %v", name, err)
	}

	return true, nil
}

// StartClusterNode learns the subnet assigned to it by the master controller
// and calls the SetupNode script which establishes the logical switch
func (cluster *OvnClusterController) StartClusterNode(name string) error {
	var err error
	var node *kapi.Node
	var subnet *net.IPNet
	var clusterSubnets []string
	var cidr string

	var readyChan = make(chan bool, 1)
	var wg sync.WaitGroup

	err = cluster.watchConfigEndpoints(readyChan)
	if err != nil {
		return err
	}

	// Hold until we are certain that the endpoint has been setup.
	// We risk polling an inactive master if we don't wait while a new leader election is on-going
	<-readyChan

	err = setupOVNNode(name)
	if err != nil {
		return err
	}

	for _, clusterSubnet := range config.Default.ClusterSubnets {
		clusterSubnets = append(clusterSubnets, clusterSubnet.CIDR.String())
	}

	messages := make(chan error)

	// First wait for the node logical switch to be created by the Master, timeout is 300s.
	err = wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
		if node, err = cluster.Kube.GetNode(name); err != nil {
			return false, fmt.Errorf("error retrieving node %s: %v", name, err)
		}
		if cidr, _, err = util.RunOVNNbctl("get", "logical_switch", node.Name, "other-config:subnet"); err != nil {
			return false, fmt.Errorf("error retrieving logical switch: %v", err)
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("timed out waiting for node's: %q logical switch: %v", name, err)
	}

	_, subnet, err = net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid hostsubnet found for node %s: %v", node.Name, err)
	}

	logrus.Infof("Node %s ready for ovn initialization with subnet %s", node.Name, subnet.String())

	mgmtPortAnnotations, err := CreateManagementPort(node.Name, subnet, clusterSubnets)
	if err != nil {
		return err
	}

	if _, err = isOVNControllerReady(name); err != nil {
		return err
	}

	type readyFunc func(string) (bool, error)
	var readyFuncs []readyFunc

	readyFuncs = append(readyFuncs, ManagementPortReady)
	wg.Add(len(readyFuncs))

	// Set management port macAddress as annotation
	if err := cluster.Kube.SetAnnotationsOnNode(node, mgmtPortAnnotations); err != nil {
		return fmt.Errorf("Failed to set node %s mgmt port macAddress annotation: %v", node.Name, mgmtPortAnnotations)
	}

	portName := "k8s-" + node.Name

	// Wait for the portMac to be created
	for _, f := range readyFuncs {
		go func(rf readyFunc) {
			defer wg.Done()
			err := wait.PollImmediate(500*time.Millisecond, 300*time.Second, func() (bool, error) {
				return rf(portName)
			})
			messages <- err
		}(f)
	}
	go func() {
		wg.Wait()
		close(messages)
	}()

	for i := range messages {
		if i != nil {
			return fmt.Errorf("Timeout error while obtaining addresses for %s (%v)", portName, i)
		}
	}

	if config.Gateway.Mode != config.GatewayModeDisabled {
		err = cluster.initGateway(node.Name, clusterSubnets, subnet.String())
		if err != nil {
			return err
		}
	}

	confFile := filepath.Join(config.CNI.ConfDir, config.CNIConfFileName)
	_, err = os.Stat(confFile)
	if os.IsNotExist(err) {
		err = config.WriteCNIConfig(config.CNI.ConfDir, config.CNIConfFileName)
		if err != nil {
			return err
		}
	}

	// start the cni server
	cniServer := cni.NewCNIServer("")
	err = cniServer.Start(cni.HandleCNIRequest)

	return err
}

func validateOVNConfigEndpoint(ep *kapi.Endpoints) bool {
	return len(ep.Subsets) == 1 && len(ep.Subsets[0].Ports) == 2 && len(ep.Subsets[0].Addresses) == 1
}

func updateOVNConfig(ep *kapi.Endpoints, readyChan chan bool) error {

	if !validateOVNConfigEndpoint(ep) {
		return fmt.Errorf("endpoint %v is not of the right format to configure OVN", ep)
	}

	var southboundDBPort string
	var northboundDBPort string
	var masterIP string

	for _, ovnDB := range ep.Subsets[0].Ports {
		if ovnDB.Name == "south" {
			southboundDBPort = strconv.Itoa(int(ovnDB.Port))
		}
		if ovnDB.Name == "north" {
			northboundDBPort = strconv.Itoa(int(ovnDB.Port))
		}
	}

	masterIP = ep.Subsets[0].Addresses[0].IP

	if err := config.UpdateOVNNodeAuth(masterIP, southboundDBPort, northboundDBPort); err != nil {
		return err
	}

	for _, auth := range []config.OvnAuthConfig{config.OvnNorth, config.OvnSouth} {
		if err := auth.SetDBAuth(); err != nil {
			return err
		}
		logrus.Infof("OVN databases reconfigured, masterIP %s, northbound-db %s, southbound-db %s", ep.Subsets[0].Addresses[0].IP, northboundDBPort, southboundDBPort)
	}

	readyChan <- true
	return nil
}

//watchConfigEndpoints starts the watching of Endpoint resource and calls back to the appropriate handler logic
func (cluster *OvnClusterController) watchConfigEndpoints(readyChan chan bool) error {
	_, err := cluster.watchFactory.AddFilteredEndpointsHandler(config.Kubernetes.OVNConfigNamespace,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				ep := obj.(*kapi.Endpoints)
				if ep.Name == "ovnkube-db" {
					if err := updateOVNConfig(ep, readyChan); err != nil {
						logrus.Errorf(err.Error())
					}
				}
			},
			UpdateFunc: func(old, new interface{}) {
				epNew := new.(*kapi.Endpoints)
				epOld := old.(*kapi.Endpoints)
				if !reflect.DeepEqual(epNew.Subsets, epOld.Subsets) && epNew.Name == "ovnkube-db" {
					if err := updateOVNConfig(epNew, readyChan); err != nil {
						logrus.Errorf(err.Error())
					}
				}
			},
		}, nil)
	return err
}
