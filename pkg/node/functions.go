package node

import (
	"context"
	"ec-csi-plugin/pkg/volumeid"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mountutil "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
	"os"
)

// NodeStageVolume mounts the volume to a staging path on the node. This is
// called by the CO before NodePublishVolume and is used to temporary mount the
// volume to a staging path. Once mounted, NodePublishVolume will make sure to mount it to the appropriate path
func (s *Service) NodeStageVolume(_ context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	methodName := "NodeStageVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}

	if req.StagingTargetPath == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: Staging Target Path must be provided", methodName)
	}

	if req.VolumeCapability == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapability must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":           req.VolumeId,
		"staging_target_path": req.StagingTargetPath,
		"method":              methodName,
	})
	log.Infof("%s is called", methodName)

	// If it is a block volume, we do nothing for stage volume
	// because we bind mount the absolute device path to a file
	switch req.VolumeCapability.GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		return &csi.NodeStageVolumeResponse{}, nil
	}

	providerID, err := volumeid.ProviderID(req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: invalid volume handle: %s", methodName, err)
	}
	source, err := s.mounter.DevicePathByVolumeID(providerID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to find device path: %s", methodName, err)
	}
	target := req.StagingTargetPath

	mnt := req.VolumeCapability.GetMount()
	options := mnt.MountFlags

	fsType := "ext4"
	if mnt.FsType != "" {
		fsType = mnt.FsType
	}

	log = s.log.WithFields(logrus.Fields{
		"volume_mode":     "filesystem",
		"volume_id":       req.VolumeId,
		"volume_context":  req.VolumeContext,
		"publish_context": req.PublishContext,
		"source":          source,
		"fs_type":         fsType,
		"mount_options":   options,
	})

	if s.validateAttachment {
		if err = s.mounter.IsAttached(source); err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to check attached: %s", methodName, err)
		}
	}

	formatted, err := s.mounter.IsFormatted(source)
	if err != nil {
		return nil, err
	}

	if !formatted {
		log.Info("formatting the volume for staging")
		if err = s.mounter.Format(source, fsType); err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to format: %s", methodName, err)
		}
	} else {
		log.Info("source device is already formatted")
	}

	log.Info("mounting the volume for staging")

	mounted, err := s.mounter.IsMounted(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to check mounted: %s", methodName, err)
	}

	if !mounted {
		if err = s.mounter.Mount(source, target, fsType, options...); err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to mount: %s", methodName, err)
		}
	} else {
		log.Info("source device is already mounted to the target path")
	}

	if _, err = os.Stat(source); err == nil {
		r := mountutil.NewResizeFs(utilexec.New())
		needResize, err := r.NeedResize(source, target)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: could not determine if volume %q need to be resized: %s", methodName, req.VolumeId, err)
		}

		if needResize {
			if _, err = r.Resize(source, target); err != nil {
				return nil, status.Errorf(codes.Internal, "%s: could not resize volume %q: %s", methodName, req.VolumeId, err)
			}
		}
	}

	log.Info("formatting and mounting stage volume is finished")
	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unstages the volume from the staging path
func (s *Service) NodeUnstageVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	methodName := "NodeUnstageVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s VolumeID must be provided", methodName)
	}

	if req.StagingTargetPath == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: StagingTargetPath must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":           req.VolumeId,
		"staging_target_path": req.StagingTargetPath,
		"method":              methodName,
	})
	log.Infof("%s is called", methodName)

	mounted, err := s.mounter.IsMounted(req.StagingTargetPath)
	if err != nil {
		return nil, err
	}

	if mounted {
		log.Info("unmounting the staging target path")
		if err = s.mounter.Unmount(req.StagingTargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to unmount: %s", methodName, err)
		}
	} else {
		log.Info("staging target path is already unmounted")
	}

	log.Info("unmounting stage volume is finished")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume mounts the volume mounted to the staging path to the target path
func (s *Service) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	methodName := "NodePublishVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}

	if req.StagingTargetPath == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: StagingTargetPath must be provided", methodName)
	}

	if req.TargetPath == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: TargetPath must be provided", methodName)
	}

	if req.VolumeCapability == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapability must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":           req.VolumeId,
		"staging_target_path": req.StagingTargetPath,
		"target_path":         req.TargetPath,
		"method":              methodName,
	})
	log.Infof("%s is called", methodName)

	options := []string{"bind"}
	if req.Readonly {
		options = append(options, "ro")
	}

	switch req.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		err := s.nodePublishVolumeForBlock(req, options, log)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to publish volume: %s", methodName, err)
		}
	case *csi.VolumeCapability_Mount:
		err := s.nodePublishVolumeForFileSystem(req, options, log)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to publish volume: %s", methodName, err)
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "%s: unknown access type", methodName)
	}

	log.Info("bind mounting the volume is finished")
	return &csi.NodePublishVolumeResponse{}, nil
}
func (s *Service) nodePublishVolumeForFileSystem(req *csi.NodePublishVolumeRequest, mountOptions []string, log *logrus.Entry) error {
	source := req.StagingTargetPath
	target := req.TargetPath

	mnt := req.VolumeCapability.GetMount()
	for _, flag := range mnt.MountFlags {
		mountOptions = append(mountOptions, flag)
	}

	fsType := "ext4"
	if mnt.FsType != "" {
		fsType = mnt.FsType
	}

	mounted, err := s.mounter.IsMounted(target)
	if err != nil {
		return err
	}

	log = s.log.WithFields(logrus.Fields{
		"source_path":   source,
		"volume_mode":   "filesystem",
		"fs_type":       fsType,
		"mount_options": mountOptions,
	})

	if !mounted {
		log.Info("mounting the volume")
		if err = s.mounter.Mount(source, target, fsType, mountOptions...); err != nil {
			return err
		}
	} else {
		log.Info("volume is already mounted")
	}

	return nil
}
func (s *Service) nodePublishVolumeForBlock(req *csi.NodePublishVolumeRequest, mountOptions []string, log *logrus.Entry) error {
	source, err := s.findAbsoluteDeviceByIDPath(req.VolumeId)
	if err != nil {
		return err
	}

	target := req.TargetPath

	mounted, err := s.mounter.IsMounted(target)
	if err != nil {
		return err
	}

	log = s.log.WithFields(logrus.Fields{
		"source_path":   source,
		"volume_mode":   "block",
		"mount_options": mountOptions,
	})

	if !mounted {
		log.Info("mounting the volume")
		if err = s.mounter.Mount(source, target, "", mountOptions...); err != nil {
			return err
		}
	} else {
		log.Info("volume is already mounted")
	}

	return nil
}

// NodeUnpublishVolume unmounts the volume from the target path
func (s *Service) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	methodName := "NodeUnpublishVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}

	if req.TargetPath == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: TargetPath must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":   req.VolumeId,
		"target_path": req.TargetPath,
		"method":      methodName,
	})
	log.Infof("%s is called", methodName)

	err := s.mounter.Unmount(req.TargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to unmount: %s", methodName, err)
	}

	log.Info("unmounting volume is finished")
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetVolumeStats returns the volume capacity statistics available for the given volume.
func (s *Service) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats VolumeID must be provided")
	}

	volumePath := req.VolumePath
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats Volume Path must be provided")
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":   req.VolumeId,
		"volume_path": req.VolumePath,
		"method":      "node_get_volume_stats",
	})
	log.Info("node get volume stats called")

	mounted, err := s.mounter.IsMounted(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check if volume path %q is mounted: %s", volumePath, err)
	}

	if !mounted {
		return nil, status.Errorf(codes.NotFound, "volume path %q is not mounted", volumePath)
	}

	isBlock, err := s.mounter.IsBlockDevice(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to determine if %q is block device: %s", volumePath, err)
	}

	stats, err := s.mounter.GetStatistics(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to retrieve capacity statistics for volume path %q: %s", volumePath, err)
	}

	// only can retrieve total capacity for a block device
	if isBlock {
		log.WithFields(logrus.Fields{
			"volume_mode": "block",
			"bytes_total": stats.TotalBytes,
		}).Info("node capacity statistics retrieved")
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:  csi.VolumeUsage_BYTES,
					Total: stats.TotalBytes,
				},
			},
		}, nil
	}

	log.WithFields(logrus.Fields{
		"volume_mode":      "filesystem",
		"bytes_available":  stats.AvailableBytes,
		"bytes_total":      stats.TotalBytes,
		"bytes_used":       stats.UsedBytes,
		"inodes_available": stats.AvailableInodes,
		"inodes_total":     stats.TotalInodes,
		"inodes_used":      stats.UsedInodes,
	}).Info("node capacity statistics retrieved")

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Available: stats.AvailableBytes,
				Total:     stats.TotalBytes,
				Used:      stats.UsedBytes,
				Unit:      csi.VolumeUsage_BYTES,
			},
			{
				Available: stats.AvailableInodes,
				Total:     stats.TotalInodes,
				Used:      stats.UsedInodes,
				Unit:      csi.VolumeUsage_INODES,
			},
		},
	}, nil
}

func (s *Service) NodeExpandVolume(_ context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	methodName := "NodeExpandVolume"
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID not provided", methodName)
	}

	volumePath := req.GetVolumePath()
	if len(volumePath) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s: volume path not provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":   req.VolumeId,
		"volume_path": req.VolumePath,
		"method":      methodName,
	})
	log.Infof("%s is called", methodName)

	if req.GetVolumeCapability() != nil {
		switch req.GetVolumeCapability().GetAccessType().(type) {
		case *csi.VolumeCapability_Block:
			log.Info("filesystem expansion is skipped for block volumes")
			return &csi.NodeExpandVolumeResponse{}, nil
		}
	}

	mounted, err := s.mounter.IsMounted(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to check if volume path %q is mounted: %s", methodName, volumePath, err)
	}

	if !mounted {
		return nil, status.Errorf(codes.NotFound, "%s: volume path %q is not mounted", methodName, volumePath)
	}

	providerID, err := volumeid.ProviderID(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: invalid volume handle: %s", methodName, err)
	}
	devicePath, err := s.mounter.DevicePathByVolumeID(providerID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: unable to get device path for %q: %s", methodName, volumePath, err)
	}

	if devicePath == "" {
		return nil, status.Errorf(codes.NotFound, "%s: device path for volume path %q not found", methodName, volumePath)
	}

	log = log.WithFields(logrus.Fields{"device_path": devicePath})
	log.Info("resizing volume")
	if _, err = mountutil.NewResizeFs(utilexec.New()).Resize(devicePath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "%s: could not resize volume %q (%q): %s", methodName, volumeID, req.GetVolumePath(), err)
	}

	log.Info("volume is resized")
	return &csi.NodeExpandVolumeResponse{}, nil
}

// NodeGetCapabilities returns the supported capabilities of the node server
func (s *Service) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	s.log.WithFields(logrus.Fields{"node_capabilities": Capabilities, "method": "nodeGetCapabilities"}).Info("NodeGetCapabilities is called")
	return &csi.NodeGetCapabilitiesResponse{Capabilities: Capabilities}, nil
}

// NodeGetInfo returns the supported capabilities of the node server. This
// should eventually return the droplet ID if possible. This is used so the CO
// knows where to place the workload. The result of this function will be used by the CO in ControllerPublishVolume.
func (s *Service) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	s.log.WithField("method", "nodeGetInfo").Info("NodeGetInfo is called")
	nodeID, err := s.metadata.InstanceID()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "NodeGetInfo: unable to retrieve instance id of node: %v", err)
	}

	//zone, err := s.metadata.AZ()
	//if err != nil {
	//	return nil, status.Error(codes.Internal, fmt.Sprintf("[NodeGetInfo] Unable to retrieve availability zone of node %v", err))
	//}
	//if err != nil || zone == "" {
	//	zone = defaultZone
	//}
	//topology := &csi.Topology{Segments: map[string]string{topologyKey: zone}}

	resp := &csi.NodeGetInfoResponse{
		NodeId:            nodeID,
		MaxVolumesPerNode: defaultMaxVolAttachLimit,
	}
	if s.topologyRegion != "" {
		resp.AccessibleTopology = &csi.Topology{
			Segments: map[string]string{"topology.kubernetes.io/region": s.topologyRegion},
		}
	}
	return resp, nil
}
