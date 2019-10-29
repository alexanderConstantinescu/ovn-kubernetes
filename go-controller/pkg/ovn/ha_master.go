package ovn

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/sirupsen/logrus"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	haLeaderLockName = "ovn-kubernetes-master"
)

// HAMasterController is the object holder for managing the HA master
// cluster
type HAMasterController struct {
	kubeClient    kubernetes.Interface
	ovnController *Controller
	nodeName      string
	isLeader      bool
	leaderElector *leaderelection.LeaderElector
}

// NewHAMasterController creates a new HA Master controller
func NewHAMasterController(kubeClient kubernetes.Interface, wf *factory.WatchFactory,
	nodeName string) *HAMasterController {
	ovnController := NewOvnController(kubeClient, wf)
	return &HAMasterController{
		kubeClient:    kubeClient,
		ovnController: ovnController,
		nodeName:      nodeName,
		isLeader:      false,
		leaderElector: nil,
	}
}

// StartHAMasterController runs the replication controller
func (hacontroller *HAMasterController) StartHAMasterController() error {
	// Set up leader election process first
	rl, err := resourcelock.New(
		resourcelock.ConfigMapsResourceLock,
		config.Kubernetes.OVNConfigNamespace,
		haLeaderLockName,
		hacontroller.kubeClient.CoreV1(),
		nil,
		resourcelock.ResourceLockConfig{
			Identity:      hacontroller.nodeName,
			EventRecorder: nil,
		})

	if err != nil {
		return err
	}

	hacontrollerOnStartedLeading := func(ctx context.Context) {
		logrus.Infof("I (%s) won the election. In active mode", hacontroller.nodeName)
		if err := hacontroller.createDBCluster(); err != nil {
			logrus.Infof("unable to create cluster: %v", err)
		}
		if err := hacontroller.ConfigureAsActive(hacontroller.nodeName); err != nil {
			panic(err.Error())
		}
		hacontroller.isLeader = true
	}

	hacontrollerOnStoppedLeading := func() {
		//This node was leader and it lost the election.
		// Whenever the node transitions from leader to follower,
		// we need to handle the transition properly like clearing
		// the cache. It is better to exit for now.
		// kube will restart and this will become a follower.
		logrus.Infof("I (%s) am no longer a leader. Exiting", hacontroller.nodeName)
		os.Exit(1)
	}

	hacontrollerOnNewLeader := func(nodeName string) {
		if nodeName != hacontroller.nodeName {
			logrus.Infof("I (%s) lost the election to %s. In Standby mode", hacontroller.nodeName, nodeName)
			if err := hacontroller.joinDBCluster(nodeName); err != nil {
				logrus.Errorf("unable to join the cluster: %v", err)
			}
		}
	}

	lec := leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: time.Duration(config.MasterHA.ElectionLeaseDuration) * time.Second,
		RenewDeadline: time.Duration(config.MasterHA.ElectionRenewDeadline) * time.Second,
		RetryPeriod:   time.Duration(config.MasterHA.ElectionRetryPeriod) * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: hacontrollerOnStartedLeading,
			OnStoppedLeading: hacontrollerOnStoppedLeading,
			OnNewLeader:      hacontrollerOnNewLeader,
		},
	}

	hacontroller.leaderElector, err = leaderelection.NewLeaderElector(lec)
	if err != nil {
		return err
	}
	go hacontroller.leaderElector.Run(context.Background())

	return nil
}

// ConfigureAsActive configures the node as active.
func (hacontroller *HAMasterController) ConfigureAsActive(masterNodeName string) error {
	// run the cluster controller to init the master
	err := hacontroller.ovnController.StartClusterMaster(hacontroller.nodeName)
	if err != nil {
		return err
	}
	return hacontroller.ovnController.Run()
}

func (hacontroller *HAMasterController) createDBCluster() error {
	localArgs, err := hacontroller.prepareArgs()
	if err != nil {
		return err
	}
	if _, err := hacontroller.execLocally(localArgs, "start_northd"); err != nil {
		return err
	}
	return nil
}

func (hacontroller *HAMasterController) joinDBCluster(masterNodeName string) error {
	masterNode, err := hacontroller.ovnController.kube.GetNode(masterNodeName)
	if err != nil {
		return err
	}

	masterAddress, err := hacontroller.getNodeAddress(*masterNode)
	if err != nil {
		return err
	}

	localArgs, err := hacontroller.prepareArgs()
	if err != nil {
		return err
	}

	clusterCmd := []string{
		fmt.Sprintf("--db-nb-addr=%s", localArgs.nodeAddress),
		"--db-nb-create-insecure-remote=yes",
		fmt.Sprintf("--db-sb-addr=%s", localArgs.nodeAddress),
		"--db-sb-create-insecure-remote=yes",
		fmt.Sprintf("--db-nb-cluster-local-addr=%s", localArgs.nodeAddress),
		fmt.Sprintf("--db-sb-cluster-local-addr=%s", localArgs.nodeAddress),
		fmt.Sprintf("--db-nb-cluster-remote-addr=%s", masterAddress),
		fmt.Sprintf("--db-sb-cluster-remote-addr=%s", masterAddress),
		fmt.Sprintf("--ovn-northd-nb-db=%s", localArgs.ovnNorthAddresses),
		fmt.Sprintf("--ovn-northd-sb-db=%s", localArgs.ovnSouthAddresses),
		"start_northd",
	}

	if _, stderr, err := util.RunOVNctl(clusterCmd...); err != nil {
		return fmt.Errorf("db creation error: %v, %s", err, stderr)
	}
	return nil
}

type localArgs struct {
	nodeAddress       string
	ovnNorthAddresses string
	ovnSouthAddresses string
}

func (hacontroller *HAMasterController) prepareArgs() (*localArgs, error) {
	node, err := hacontroller.ovnController.kube.GetNode(hacontroller.nodeName)
	if err != nil {
		return nil, err
	}

	nodeAddress, err := hacontroller.getNodeAddress(*node)
	if err != nil {
		return nil, err
	}

	ovnNorthAddresses, ovnSouthAddresses, err := hacontroller.getOVNAddresses()
	if err != nil {
		return nil, err
	}
	return &localArgs{nodeAddress, ovnNorthAddresses, ovnSouthAddresses}, nil
}

func (hacontroller *HAMasterController) execLocally(localArgs *localArgs, cmd string) (string, error) {
	clusterCmd := []string{
		fmt.Sprintf("--db-nb-addr=%s", localArgs.nodeAddress),
		"--db-nb-create-insecure-remote=yes",
		fmt.Sprintf("--db-sb-addr=%s", localArgs.nodeAddress),
		"--db-sb-create-insecure-remote=yes",
		fmt.Sprintf("--db-nb-cluster-local-addr=%s", localArgs.nodeAddress),
		fmt.Sprintf("--db-sb-cluster-local-addr=%s", localArgs.nodeAddress),
		fmt.Sprintf("--ovn-northd-nb-db=%s", localArgs.ovnNorthAddresses),
		fmt.Sprintf("--ovn-northd-sb-db=%s", localArgs.ovnSouthAddresses),
		cmd,
	}

	stdout, stderr, err := util.RunOVNctl(clusterCmd...)
	if err != nil && err.Error() != "exit status 1" {
		return "", fmt.Errorf("db command error: %v, %s", err, stderr)
	}
	return stdout, nil
}

func (hacontroller *HAMasterController) getNodeAddress(node kapi.Node) (string, error) {
	for _, address := range node.Status.Addresses {
		if address.Type == kapi.NodeInternalIP {
			return address.Address, nil
		}
	}
	return "", fmt.Errorf("node does not have an internalIP: %v", node)
}

func (hacontroller *HAMasterController) getOVNAddresses() (string, string, error) {
	requirement, err := labels.NewRequirement("node-role.kubernetes.io/master", selection.Equals, []string{""})
	if err != nil {
		return "", "", fmt.Errorf("unable to create requirement label: %v", err)
	}

	selector := labels.NewSelector().Add(*requirement)

	masterNodes, err := hacontroller.ovnController.kube.GetNodesByLabels(selector)
	if err != nil {
		return "", "", fmt.Errorf("unable to retrieve master nodes: %v", err)
	}

	ovnNorthAddresses, ovnSouthAddresses := "", ""
	for _, masterNode := range masterNodes.Items {
		address, err := hacontroller.getNodeAddress(masterNode)
		if err != nil {
			return "", "", fmt.Errorf("unable to retrieve cluster's master node address: %v", err)
		}
		ovnNorthAddresses += fmt.Sprintf("%s:%s:%v,", config.OvnDBSchemeTCP, address, config.MasterHA.NbPort)
		ovnSouthAddresses += fmt.Sprintf("%s:%s:%v,", config.OvnDBSchemeTCP, address, config.MasterHA.SbPort)
	}

	ovnNorthAddresses = ovnNorthAddresses[0 : len(ovnNorthAddresses)-1]
	ovnSouthAddresses = ovnSouthAddresses[0 : len(ovnSouthAddresses)-1]

	return ovnNorthAddresses, ovnSouthAddresses, nil
}
