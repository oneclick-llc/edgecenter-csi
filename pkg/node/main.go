package node

import (
	"ec-csi-plugin/pkg/metadata"
	"ec-csi-plugin/pkg/mounter"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
)

func New(mounter mounter.Interface, metadata metadata.Interface, validateAttachment bool, topologyRegion string, log *logrus.Entry) csi.NodeServer {
	return &Service{
		mounter:            mounter,
		metadata:           metadata,
		validateAttachment: validateAttachment,
		topologyRegion:     topologyRegion,
		log:                log,
	}
}
