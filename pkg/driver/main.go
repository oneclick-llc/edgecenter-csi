package driver

import (
	"ec-csi-plugin/pkg/controller"
	"ec-csi-plugin/pkg/identity"
	"ec-csi-plugin/pkg/metadata"
	"ec-csi-plugin/pkg/mounter"
	"ec-csi-plugin/pkg/node"
	"ec-csi-plugin/pkg/version"
	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	"log"
	"net"
	"os"
)

const defaultDriverName = "csi.edgecenter.org"

func New(clouds map[int]*edgecloudV2.Client, regionTopologies map[int]string, clusterID string, legacyRegionID int64, validateAttachments bool) NonBlockingGRPCServer {
	d := &Driver{}

	d.log = logrus.New().WithFields(logrus.Fields{})

	driverName := os.Getenv("CSI_DRIVER_NAME")
	if driverName == "" {
		driverName = defaultDriverName
	}
	topologyRegion := os.Getenv("CSI_TOPOLOGY_REGION")
	if topologyRegion == "" && len(regionTopologies) == 1 {
		for _, topology := range regionTopologies {
			topologyRegion = topology
		}
	}

	d.identityServer = identity.New(driverName, version.Version)
	regions := make([]controller.Region, 0, len(clouds))
	for regionID, cloud := range clouds {
		regions = append(regions, controller.Region{
			ID:       int64(regionID),
			Topology: regionTopologies[regionID],
			Cloud:    cloud,
		})
	}
	d.controllerServer = controller.NewMulti(regions, clusterID, legacyRegionID, d.log)
	d.nodeServer = node.New(mounter.New(d.log), metadata.New(), validateAttachments, topologyRegion, d.log)

	return d
}

func (d *Driver) Start(endpoint string) {
	d.wg.Add(1)
	go func() {
		proto, addr, err := parseEndpoint(endpoint)
		if err != nil {
			klog.Fatal(err.Error())
		}

		if proto == "unix" {
			addr = "/" + addr
			if err = os.Remove(addr); err != nil && !os.IsNotExist(err) {
				klog.Fatalf("Failed to remove %s, error: %s", addr, err.Error())
			}
		}

		listener, err := net.Listen(proto, addr)
		if err != nil {
			klog.Fatalf("Failed to listen: %v", err)
		}

		opts := []grpc.ServerOption{
			grpc.UnaryInterceptor(d.logGRPC),
		}
		d.server = grpc.NewServer(opts...)

		log.Println("registry identity server")
		csi.RegisterIdentityServer(d.server, d.identityServer)

		log.Println("registry controller server")
		csi.RegisterControllerServer(d.server, d.controllerServer)

		log.Println("registry node server")
		csi.RegisterNodeServer(d.server, d.nodeServer)

		klog.Infof("Listening for connections on address: %#v", listener.Addr())

		if err = d.server.Serve(listener); err != nil {
			klog.Infof("Driver stopped with: %v", err)
			return
		}
	}()
}

func (d *Driver) Wait() {
	d.wg.Wait()
}

func (d *Driver) Stop() {
	d.server.GracefulStop()
}

func (d *Driver) ForceStop() {
	d.server.Stop()
}
