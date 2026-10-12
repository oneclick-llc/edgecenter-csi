package node

import (
	"ec-csi-plugin/pkg/metadata"
	"ec-csi-plugin/pkg/mounter"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
)

type Service struct {
	mounter  mounter.Interface
	metadata metadata.Interface

	log *logrus.Entry

	validateAttachment bool
	topologyRegion     string
	csi.UnimplementedNodeServer
}
