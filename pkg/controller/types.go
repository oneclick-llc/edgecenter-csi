package controller

import (
	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
)

type Service struct {
	cloud *edgecloudV2.Client

	log *logrus.Entry

	clusterID      string
	topologyRegion string
	csi.UnimplementedControllerServer
}
