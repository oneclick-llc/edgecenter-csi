package controller

import (
	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
)

func New(cloud *edgecloudV2.Client, clusterID string, topologyRegion string, log *logrus.Entry) csi.ControllerServer {
	return &Service{cloud: cloud, clusterID: clusterID, topologyRegion: topologyRegion, log: log}
}
