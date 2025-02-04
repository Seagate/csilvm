package csilvm

import (
	"bytes"
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"github.com/Seagate/csiclvm/pkg/lvm"
	"github.com/Seagate/csiclvm/pkg/version"
	"github.com/Seagate/csiclvm/pkg/virsh"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/uber-go/tally"
	"golang.org/x/net/context"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
)

const (
	topologyKey = ".speedboat.seagate.com/nodeId"
)

type Server struct {
	vgname               string
	pvnames              []string
	volumeGroup          *lvm.VolumeGroup
	defaultVolumeSize    uint64
	supportedFilesystems map[string]string
	removingVolumeGroup  bool
	controllerMode       bool
	tags                 []string
	probeModules         map[string]struct{}
	nodeID               string
	metrics              tally.Scope
}

// NewServer returns a new Server that will manage the given LVM volume
// group. It accepts a variadic list of ServerOpt with which the server's
// default options can be overwritten. The Setup method must be called before
// any other further method calls are performed in order to setup/remove the
// volume group.
func NewServer(vgname string, pvnames []string, defaultFs string, opts ...ServerOpt) *Server {
	const (
		// Unless overwritten by the DefaultVolumeSize
		// ServerOpt the default size for new volumes is
		// 10GiB.
		defaultVolumeSize = 10 << 30
	)
	s := &Server{
		vgname:            vgname,
		pvnames:           pvnames,
		defaultVolumeSize: defaultVolumeSize,
		supportedFilesystems: map[string]string{
			"":        defaultFs,
			defaultFs: defaultFs,
		},
		metrics:   tally.NoopScope,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(s)
	}

	// Set default tags on metrics.
	s.metrics = s.metrics.Tagged(map[string]string{
		"volume-group": s.vgname,
	})

	log.Printf("NewServer: %v", s)
	return s
}

func (s *Server) SupportedFilesystems() map[string]string {
	m := make(map[string]string)
	for k, v := range s.supportedFilesystems {
		m[k] = v
	}
	return m
}

func (s *Server) RemovingVolumeGroup() bool {
	return s.removingVolumeGroup
}

func (s *Server) ControllerMode() bool {
	return s.controllerMode
}

type ServerOpt func(*Server)

func NodeID(nid string) ServerOpt {
	return func(s *Server) {
		s.nodeID = nid
	}
}

// DefaultVolumeSize sets the default size in bytes of new volumes if
// no volume capacity is specified. To specify that a new volume
// should consist of all available space on the volume group you can
// pass `lvm.MaxSize` to this option.
func DefaultVolumeSize(size uint64) ServerOpt {
	return func(s *Server) {
		s.defaultVolumeSize = size
	}
}

func SupportedFilesystem(fstype string) ServerOpt {
	if fstype == "" {
		panic("csilvm: SupportedFilesystem: filesystem type not provided")
	}
	return func(s *Server) {
		s.supportedFilesystems[fstype] = fstype
	}
}

// RemoveVolumeGroup configures the Server to operate in "remove" mode. The
// volume group will be removed when the server starts. Most RPCs will return
// an error if the plugin is started in this mode.
func RemoveVolumeGroup() ServerOpt {
	return func(s *Server) {
		s.removingVolumeGroup = true
	}
}

// ControllerMode configures the Server to operate as CSI Controller Agent.
func ControllerMode() ServerOpt {
	return func(s *Server) {
		s.controllerMode = true
	}
}


// Tag configures the volume group with the specified tag. Any volumes
// that are created will be tagged with the volume group tags.
func Tag(tag string) ServerOpt {
	return func(s *Server) {
		s.tags = append(s.tags, tag)
	}
}

// Metrics sets the Server's tally.Scope, used for reporting metrics.
func Metrics(scope tally.Scope) ServerOpt {
	return func(s *Server) {
		s.metrics = scope
	}
}

// ProbeModules configures the server to query the loaded kernel modules to ensure
// that prerequisite modules are loaded before any operations are executed.
// This option may be specified multiple times to append additional module requirements.
func ProbeModules(required []string) ServerOpt {
	if len(required) == 0 {
		return nil
	}
	m := make(map[string]struct{})
	for _, r := range required {
		m[r] = struct{}{}
	}
	return func(s *Server) {
		if s.probeModules == nil {
			s.probeModules = make(map[string]struct{}, len(m))
		}
		for k := range m {
			s.probeModules[k] = struct{}{}
		}
	}
}

// Setup checks that the specified volume group exists, creating it if it does
// not. If the RemoveVolumeGroup option is set this method removes the volume
// group.
func (s *Server) Setup() error {
	log.Printf("Validating tags: %v", s.tags)
	for _, tag := range s.tags {
		if err := lvm.ValidateTag(tag); err != nil {
			return fmt.Errorf(
				"Invalid tag '%v': err=%v",
				tag,
				err)
		}
	}
	log.Printf("Checking StoLake Agent version..." )
	stolakeVer, err := virsh.StoLakeInfo()
	if  err != nil {
		log.Printf("Stolake Agent Not found %v ",err)
		return fmt.Errorf( "Stolake Agent not found  err=%v", err)
	}
	log.Printf("STOLAKE VERSION: %s", stolakeVer)
	log.Printf("Looking up volume group %v", s.vgname)
	volumeGroup, err2 := lvm.LookupVolumeGroup(s.vgname)
	if err2 != nil {
		//return fmt.Errorf( "Cannot lookup volume group %v: err=%v", s.vgname, err)
		log.Printf( "Cannot lookup volume group %v: err=%v", s.vgname, err)
	}else{
		log.Printf("Found volume group %v. Starting Locks", s.vgname)
		err := virsh.VgActivate(s.vgname)
		if err != nil {
			log.Printf( "FAILED to start start VG lock for %v :: err=%v", s.vgname, err)
		}
	}
	s.volumeGroup = volumeGroup
	return nil
}

// IdentityService RPCs

const (
	manifestBuildSHA  = "buildSHA"
	manifestBuildTime = "buildTime"
)

func (s *Server) GetPluginInfo(
	ctx context.Context,
	request *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {

	v := version.Get()
	m := make(map[string]string)
	if v.BuildSHA != "" {
		m[manifestBuildSHA] = v.BuildSHA
	}
	if v.BuildTime != "" {
		m[manifestBuildTime] = v.BuildTime
	}
	tenant := s.vgname[5:len(s.vgname)]

	response := &csi.GetPluginInfoResponse{
		Name:          tenant + v.Product,
		VendorVersion: v.Version,
		Manifest:      m,
	}

	return response, nil
}

func (s *Server) GetPluginCapabilities(
	ctx context.Context,
	request *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	agentmode := csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS
	if s.controllerMode {
		agentmode = csi.PluginCapability_Service_CONTROLLER_SERVICE
	}
	response := &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: agentmode,
					},
				},
			},
		},
	}
	return response, nil
}

// Probe is currently a no-op.
func (s *Server) Probe(
	ctx context.Context,
	request *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	if len(s.probeModules) > 0 {
		mods := make(map[string]struct{})
		listed, err := listModules()
		if err != nil {
			return nil, status.Errorf(
				codes.FailedPrecondition,
				"Cannot resolve kernel modules: err=%v",
				err)
		}
		for _, m := range listed {
			mods[m] = struct{}{}
		}
		var missing []string
		for m := range s.probeModules {
			if _, found := mods[m]; found {
				continue
			}
			missing = append(missing, m)
		}
		if len(missing) > 0 {
			return nil, status.Errorf(
				codes.FailedPrecondition,
				"One or more kernel modules are missing: %v",
				missing)
		}
	}
	if s.removingVolumeGroup {
		// We're busy removing the volume-group so no need to perform health checks.
		response := &csi.ProbeResponse{}
		return response, nil
	}
	log.Printf("Looking up volume group %v", s.vgname)
	_, err := lvm.LookupVolumeGroup(s.vgname)
	if err != nil {
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"Cannot find volume group %v",
			s.vgname)
	}

	response := &csi.ProbeResponse{}
	return response, nil
}

// ControllerService RPCs

func ErrNotMultipleOfExtentSize(extentSize uint64) error {
	return status.Error(codes.OutOfRange, fmt.Sprintf("Volume capacity must be a multiple of %dMiB", extentSize>>20))
}

var ErrVolumeAlreadyExists = status.Error(codes.AlreadyExists, "The volume already exists")
var ErrInsufficientCapacity = status.Error(codes.OutOfRange, "Not enough free space")
var ErrTooFewDisks = status.Error(codes.OutOfRange, "The volume group does not have enough underlying physical devices to support the requested RAID configuration")

const attrTags = "tags"

func (s *Server) volumeAttributes(lv *lvm.LogicalVolume) (map[string]string, error) {
	t, err := lv.Tags()
	if err != nil {
		return nil, err
	}
	if len(t) == 0 {
		return nil, nil
	}
	buf, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		attrTags: base64.RawURLEncoding.EncodeToString(buf),
	}, nil
}

func (s *Server) CreateVolume(
	ctx context.Context,
	request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {

	// Record the original volume name as a tag.
	encodedName := s.volumeNameToTag(request.GetName())
	tags := make([]string, len(s.tags), len(s.tags)+1)
	copy(tags, s.tags)
	tags = append(tags, encodedName)

	// Check whether a logical volume with the given name already
	// exists in this volume group.
	log.Printf("Determining whether volume %q with encoded name %v already exists", request.GetName(), encodedName)
	if lv, err := s.volumeGroup.FindLogicalVolume(lvm.LVMatchTag(encodedName)); err == nil {
		log.Printf("Volume %s already exists.", encodedName)
		// The volume already exists. Determine whether or not the
		// existing volume satisfies the request. If so, return a
		// successful response. If not, return ErrVolumeAlreadyExists.
		if err := s.validateExistingVolume(lv, request); err != nil {
			return nil, err
		}
		attr, err := s.volumeAttributes(lv)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get volume attributes: err=%v", err)
		}
		response := &csi.CreateVolumeResponse{
			Volume: &csi.Volume{
				CapacityBytes: int64(lv.SizeInBytes()),
				VolumeId:      lv.Name(),
				VolumeContext: attr,
			},
		}
		return response, nil
	}
	// Generate a random volume name and ensure that it doesn't already exist.
	var volumeID string
	const lvPrefix = "csilv"
	for i := 0; i < 10 && volumeID == ""; i++ {
		// prefix a random number to avoid stomping on reserved names.
		tryID := lvPrefix + strconv.FormatUint(rand.Uint64(), 36)
		log.Printf("Attempting to allocate id=%v for requested volume %q", tryID, request.GetName())
		if _, err := s.volumeGroup.LookupLogicalVolume(tryID); err == nil {
			log.Printf("Volume id %s already exists, trying again..", tryID)
			continue
		}
		volumeID = tryID
	}
	if volumeID == "" {
		return nil, status.Error(codes.Internal, "Failed to allocate volume ID")
	}
	log.Printf("Volume with id=%v does not already exist", volumeID)
	params := dupParams(request.GetParameters())
	layout, err := takeVolumeLayoutFromParameters(params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Invalid volume layout: err=%v", err)
	}
	// Determine the capacity, default to maximum size.
	size := s.defaultVolumeSize
	if capacityRange := request.GetCapacityRange(); capacityRange != nil {
		// Set the volume size to the minimum requested size.
		size = uint64(capacityRange.GetRequiredBytes())
		// Get the extentSize for this volume group. The LV size must be a multiple of the extent size.
		extentSize, err := s.volumeGroup.ExtentSize()
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"Error in ExtentSize: err=%v",
				err)
		}
		// If size is not already a multiple of extentSize, round it up to the
		// nearest extentSize.
		if size%extentSize != 0 {
			sizeBefore := size
			size = ((size + extentSize) / extentSize) * extentSize
			log.Printf("Rounding size up from required_bytes (about %dMiB) to nearest extent size (%dMiB) to get (%dMiB)", sizeBefore>>20, extentSize>>20, size>>20)
		}
		// Get bytesFree, it is a multiple of extentSize.
		bytesFree, err := s.volumeGroup.BytesFree(layout)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"Error in BytesFree: err=%v",
				err)
		}
		log.Printf("BytesFree: %v (%dMiB)", bytesFree, bytesFree>>20)
		// Check whether there is enough free space available.
		// bytesFree is a multiple of extentSize.
		if bytesFree < size {
			return nil, ErrInsufficientCapacity
		}
		if limit := capacityRange.GetLimitBytes(); limit != 0 && size > uint64(limit) {
			// We've already checked that there is sufficient capacity. The only
			// way we can arrive here is if [required_bytes,limit_bytes] does
			// not include a multiple of extentSize, in which case we cannot
			// satisfy this request.
			return nil, ErrNotMultipleOfExtentSize(extentSize)
		}
	}
	lvopts, err := volumeOptsFromParameters(request.GetParameters())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid parameters: %v", err)
	}

	log.Printf("Creating logical volume id=%v, size=%v, tags=%v, params=%v", volumeID, size, tags, request.GetParameters())
	lv, err := s.volumeGroup.CreateLogicalVolume(volumeID, size, tags, lvopts...)
	if err != nil {
		if err == lvm.ErrInvalidLVName {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"The volume name is invalid: err=%v",
				err)
		}
		if err == lvm.ErrNoSpace {
			// Somehow, despite checking for sufficient space
			// above, we still have insuffient free space.
			return nil, ErrInsufficientCapacity
		}
		if err == lvm.ErrTooFewDisks {
			return nil, ErrTooFewDisks
		}
		return nil, status.Errorf(
			codes.Internal,
			"Error in CreateLogicalVolume: err=%v",
			err)
	}
	attr, err := s.volumeAttributes(lv)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get volume attributes: err=%v", err)
	}

	// Pass on QOS in Volume Context for ControllerPublish
	iopspergb, ok := params["iopspergb"]
	if ok {
		attr["iopspergb"] = iopspergb
	}
	mbpspergb, okk := params["mbpspergb"]
	if okk {
		attr["mbpspergb"] = mbpspergb
	}
	// Pass on datapath mode for ControllerPublish
	datapath, okkk := params["datapath"]
	if okkk {
		attr["datapath"] = strings.ToLower(datapath)
	} else {
		attr["datapath"] = "direct" 
	}
	jbofs, ok4 := params["stolakejobfurls"]
	if ok4 {
		attr["stolakejobfurls"] = jbofs
	}

	defer s.reportStorageMetrics()
	response := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			CapacityBytes: int64(lv.SizeInBytes()),
			VolumeId:      volumeID,
			VolumeContext: attr,
		},
	}
	return response, nil
}

func (s *Server) validateExistingVolume(lv *lvm.LogicalVolume, request *csi.CreateVolumeRequest) error {
	// Determine whether the existing volume satisfies the capacity_range
	// of the current request.
	if capacityRange := request.GetCapacityRange(); capacityRange != nil {
		// If required_bytes is specified, is that requirement
		// satisfied by the existing volume?
		if requiredBytes := capacityRange.GetRequiredBytes(); requiredBytes != 0 {
			if requiredBytes > int64(lv.SizeInBytes()) {
				log.Printf("Existing volume does not satisfy request: required_bytes > volume size (%d > %d)", requiredBytes, lv.SizeInBytes())
				// The existing volume is not big enough.
				return ErrVolumeAlreadyExists
			}
		}
		if limitBytes := capacityRange.GetLimitBytes(); limitBytes != 0 {
			if limitBytes < int64(lv.SizeInBytes()) {
				log.Printf("Existing volume does not satisfy request: limit_bytes < volume size (%d < %d)", limitBytes, lv.SizeInBytes())
				// The existing volume is too big.
				return ErrVolumeAlreadyExists
			}
		}
		// We know that one of limit_bytes or required_bytes was
		// specified, thanks to the specification and the request
		// validation logic.
	}
	// The existing volume matches the requested capacity_range.  We
	// determine whether the existing volume satisfies all requested
	// volume_capabilities.
	sourcePath, err := lv.Path()
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"Error in Path(): err=%v",
			err)
	}
	log.Printf("Volume path is %v", sourcePath)
	// Removed FS type check for mount compatibility for idempotency.
	// If the agent takes too long servicing the first create volume,
	// the orchestrator will issue a 2nd create.  following the old
	// logic it would test FS type which fails because it LV is not active.
	// Checking the mount capatibility during the create is wrong since the
	// Server running the Controller could be a different OS than the node
	// that the volume will be published on.
	return nil
}

var ErrVolumeNotFound = status.Error(codes.NotFound, "The volume does not exist.")

func (s *Server) DeleteVolume(
	ctx context.Context,
	request *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	id := request.GetVolumeId()
	log.Printf("Looking up volume with id=%v", id)
	lv, err := s.volumeGroup.LookupLogicalVolume(id)
	if err != nil {
		// It is idempotent to succeed if a volume is not found.
		response := &csi.DeleteVolumeResponse{}
		return response, nil
	}
	// LVs most likely not mounted on this host.  Skipping stat'ing path
	//log.Printf("Determining volume path")
	//path, err := lv.Path()
	//if err != nil {
	//	return nil, status.Errorf(
	//		codes.Internal,
	//		"Error in Path(): err=%v",
	//		err)
	//}
	//if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
	//	return nil, status.Errorf(
	//		codes.Internal,
	//		"The device path does not exist, cannot zero volume contents. To bypass the zeroing of the volume contents, ensure the file exists, or create it by hand, and reissue the DeleteVolume operation. path=%s",
	//		path)
	//}
	// Removing feature to overwrite data since not mounted. This should be done with a K8s PV claim wiper.
	//log.Printf("Deleting data on device %v", path)
	//if err := deleteDataOnDevice(path); err != nil {
	//	return nil, status.Errorf(
	//		codes.Internal,
	//		"Cannot delete data from device: err=%v",
	//		err)
	//}
	log.Printf("Removing volume")
	if err := lv.Remove(); err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"Failed to remove volume: err=%v",
			err)
	}
	defer s.reportStorageMetrics()
	response := &csi.DeleteVolumeResponse{}
	return response, nil
}

func deleteDataOnDevice(devicePath string) error {
	// This method is the go equivalent of
	// `dd if=/dev/zero of=PhysicalVolume`.
	file, err := os.OpenFile(devicePath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	devzero, err := os.Open("/dev/zero")
	if err != nil {
		return err
	}
	defer devzero.Close()
	if _, err := io.Copy(file, devzero); err != nil {
		// We expect to stop when we get ENOSPC.
		if perr, ok := err.(*os.PathError); ok && perr.Err == syscall.ENOSPC {
			return nil
		}
		return err
	}
	panic("csilvm: expected ENOSPC when erasing data")
}

var ErrCallNotImplemented = status.Error(codes.Unimplemented, "That RPC is not implemented.")
var ErrUnsupportDatapath = status.Error(codes.NotFound, "The datapath mode is not supported.")

// Assume ControllerPublish is only called for instances running with direct attachment to drives or
// a path to call the StoLake agent
func (s *Server) ControllerPublishVolume(
	ctx context.Context,
	req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {

	// Pass QOS from Volume Context from Vol Create in Publish Context
	pubcontext := dupParams(req.GetVolumeContext())

	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "no volume ID provided")
	}

	nodeID := req.GetNodeId()
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "no node ID provided")
	}

	// VolumeCapability not needed for controller Publish but will be needed later by Node Publish
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "no volume capabilities provided")
	}
	switch strings.ToLower(pubcontext["datapath"]) {
		// Export LV as ISCSI Target on this controller node
		case "iscsi": {
			lv, err := s.volumeGroup.LookupLogicalVolume(volumeID)
			if err != nil {
				log.Printf("ControllerPublish could not find volume with id=%v", volumeID)
				return nil, ErrVolumeNotFound
			}

			// Pass Initiator IQN from NodeID and LV UUID to Staging Stolake 
			lvuuid, err :=  lv.Uuid()
			if  err != nil {
				log.Printf("ControllerPublish could not find UUID for %v", volumeID)
				return nil, ErrVolumeNotFound
			}
			// Activate the LV for targetcli to use
			err = lv.Activate()
			if err != nil {
				log.Printf("Failed to Activate LV on Controller Node for iSCSI Target lvuuid %s  %v", lvuuid, err)
				return nil, ErrVolumeNotFound
			}
			log.Printf("Setting Up iSCSI Target for %s to %s ", lvuuid, nodeID)
			targetiqn, lun, targetportal, err2 := virsh.StageIscsiTarget(lvuuid,nodeID)
			if  err2 != nil {
				log.Printf("SCSI Target Setup Error with lvuuid %s, iqn %s >> %v", lvuuid, nodeID, err2)
				return nil, ErrVolumeNotFound
			}
			pubcontext["blockid"] = targetiqn
			pubcontext["lun"] = lun
			pubcontext["portal"] = targetportal
			return  &csi.ControllerPublishVolumeResponse{PublishContext: pubcontext}, nil
		}
		// JBOF ISCSI Mode: Controller agent creates iscsi targets and passes list of targets back in pubcontext 
		case "jbofis": {
			// Validate list of 1 or more servers running stolake as a target builder service emulating a JBOF
			stolakeURLs, ok := pubcontext["stolakejobfurls"]
			if !ok  {
				return nil, status.Error(codes.InvalidArgument, "Missing stolakejobfurls parameter in storage class")
			}
			log.Printf("Setting Up iSCSI Targets for % on  %s for %s ", s.vgname, stolakeURLs, nodeID)
			targetlist, err2 := virsh.JbofStageIscsiTargets(s.vgname, stolakeURLs, nodeID)
			if  err2 != nil {
				log.Printf("SCSI Target Setup Error %v", err2)
				return nil, ErrVolumeNotFound
			}

			pubcontext["blockid"] =  "unknown at CtrlPub phase"
			pubcontext["targetlist"] = targetlist
			return  &csi.ControllerPublishVolumeResponse{PublishContext: pubcontext}, nil
		}
		case "nvme":
			return nil, ErrUnsupportDatapath
		case "qemu":
			return nil, ErrUnsupportDatapath
		case "direct":
			fallthrough
		default:
			pubcontext["blockid"] = "notneeded"
			response := &csi.ControllerPublishVolumeResponse{PublishContext: pubcontext}
			return response, nil
	}
	return nil, ErrUnsupportDatapath

//	//Validate Domain Name
//	if !virsh.IsDomValid(nodeID) {
//		return nil, status.Error(codes.NotFound, "Unknown nodeid doesn't map to oVirt DOM.")
//	}
//	// Not using virsh pools because it doesn't activate VGs with shared locks
//	// Assume VG is started with shared locks.
//
//	// Perform Mapping of vg/lv in to VM block device
//	blkid, err2 := virsh.AttachDisk(nodeID, s.vgname, volumeID)
//	if err2 != nil {
//		msg := fmt.Sprintf("Failed to Attach %s to %s\n%v\n", s.vgname, volumeID, err2)
//		return nil, status.Error(codes.Internal, msg)
//	}
//	pubcontext["blockid"] = blkid
//	response := &csi.ControllerPublishVolumeResponse{PublishContext: pubcontext}
//	return response, nil

}

func (s *Server) ControllerUnpublishVolume(
	ctx context.Context,
	request *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {

	nodeid := request.GetNodeId()
	response := &csi.ControllerUnpublishVolumeResponse{}
	if nodeid == "" {
		return response, status.Error(codes.InvalidArgument, "no node ID provided")
	}

	volumeID := request.GetVolumeId()
	lv, err := s.volumeGroup.LookupLogicalVolume(volumeID)
	if err != nil {
		//NOTE: The CSI spec say to reply with error if the volume is  "is not assumed to be ControllerUnpublished"
		// If the lv was not found we assume it has been unpublished
		return response, nil
		//return response, ErrVolumeNotFound
	}

	// FIXME: Need to discover how the volume is published to the node and undo it selectively 
	//        but for now unstage and ignore errors
	lvuuid, _ :=  lv.Uuid()
	virsh.UnStageIscsiTarget(lvuuid,nodeid)
	lv.Deactivate()

	return  &csi.ControllerUnpublishVolumeResponse{}, nil

	//FIXME  DEAD code
	if !virsh.ProxyMode() {
		response := &csi.ControllerUnpublishVolumeResponse{}
		return response, nil
	}

	//Validate Domain Name
	if nodeid == "" {
		return nil, status.Error(codes.InvalidArgument, "no node ID provided")
	}
	if !virsh.IsDomValid(nodeid) {
		return nil, status.Error(codes.NotFound, "Unknown nodeid "+nodeid+" doesn't map to oVirt DOM.")
	}
	_, err = s.volumeGroup.LookupLogicalVolume(volumeID)
	if err != nil {
		return nil, ErrVolumeNotFound
	}
	err = virsh.DetachDisk(nodeid, s.vgname, volumeID)
	if err != nil {
		msg := fmt.Sprintf("Failed to UnPublish for %s\n %v\n", volumeID, err)
		return nil, status.Error(codes.Internal, msg)
	}
	return response, nil
}

var ErrMismatchedFilesystemType = status.Error(
	codes.InvalidArgument,
	"The requeed fs_type does not match the existing filesystem on the volume.")

func (s *Server) ValidateVolumeCapabilities(
	ctx context.Context,
	request *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	id := request.GetVolumeId()
	log.Printf("Looking up volume with id=%v", id)
	lv, err := s.volumeGroup.LookupLogicalVolume(id)
	if err != nil {
		return nil, ErrVolumeNotFound
	}
	log.Printf("Determining volume path")
	sourcePath, err := lv.Path()
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"Error in Path(): err=%v",
			err)
	}
	log.Printf("Determining filesystem type at %v", sourcePath)
	existingFstype, err := determineFilesystemType(sourcePath)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"Cannot determine filesystem type: err=%v",
			err)
	}
	log.Printf("Existing filesystem type is '%v'", existingFstype)
	for _, capability := range request.GetVolumeCapabilities() {
		if mnt := capability.GetMount(); mnt != nil {
			if existingFstype != "" {
				// The volume has already been formatted.
				if mnt.GetFsType() != "" && existingFstype != mnt.GetFsType() {
					// The requested fstype does not match the existing one.
					return nil, ErrMismatchedFilesystemType
				}
			}
		}
	}
	response := &csi.ValidateVolumeCapabilitiesResponse{
		// TODO: Add optional Confirmed field
		Message: "",
	}
	return response, nil
}

const (
	tagVolumeNameEncodedPrefix = "VN+" // used when volume name is not tag-safe
	tagVolumeNamePlainPrefix   = "VN." // used when volume name is tag-safe
)

var tagSafeChars map[rune]struct{} = func() map[rune]struct{} {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_+.-1234567890"
	m := make(map[rune]struct{})
	for _, r := range safe {
		m[r] = struct{}{}
	}
	return m
}()

// volumeNameToTag attempts to preserve the suggested volume name as a suffix of the
// returned string, unless it contains unsafe chars in which case it is encoded.
func (s *Server) volumeNameToTag(volname string) string {
	for _, r := range volname {
		if _, ok := tagSafeChars[r]; ok {
			continue
		}
		return tagVolumeNameEncodedPrefix +
			base64.RawURLEncoding.EncodeToString([]byte(volname))
	}
	return tagVolumeNamePlainPrefix + volname
}

func (s *Server) ListVolumes(
	ctx context.Context,
	request *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	if s.removingVolumeGroup {
		log.Printf("Running with '-remove-volume-group', reporting no volumes")
		response := &csi.ListVolumesResponse{}
		return response, nil
	}
	//Error if starting token is offered - Needed to pass csi-sanity ListVolume with invalid start token
	if request.GetStartingToken() != "" {
		return nil, status.Errorf(codes.Aborted, "Starting_Token field not implemented.")
	}
	volnames, err := s.volumeGroup.ListLogicalVolumeNames()
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"Cannot list volume names: err=%v",
			err)
	}
	var entries []*csi.ListVolumesResponse_Entry
	for _, volname := range volnames {
		log.Printf("Looking up volume '%v'", volname)
		lv, err := s.volumeGroup.LookupLogicalVolume(volname)
		if err != nil {
			return nil, ErrVolumeNotFound
		}
		attr, err := s.volumeAttributes(lv)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get volume attributes: err=%v", err)
		}
		info := &csi.Volume{
			CapacityBytes: int64(lv.SizeInBytes()),
			VolumeId:      lv.Name(),
			VolumeContext: attr,
		}
		log.Printf("Found volume %v (%v bytes)", volname, lv.SizeInBytes())
		entry := &csi.ListVolumesResponse_Entry{Volume: info}
		entries = append(entries, entry)
	}
	defer s.reportStorageMetrics()
	response := &csi.ListVolumesResponse{
		Entries:   entries,
		NextToken: "",
	}
	return response, nil
}

func (s *Server) GetCapacity(
	ctx context.Context,
	request *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	if s.removingVolumeGroup {
		log.Printf("Running with '-remove-volume-group', reporting 0 capacity")
		// We report 0 capacity if configured to remove the volume group.
		response := &csi.GetCapacityResponse{AvailableCapacity: 0}
		return response, nil
	}
	for _, volumeCapability := range request.GetVolumeCapabilities() {
		// Check for unsupported filesystem type in order to return 0
		// capacity if it isn't supported.
		if mnt := volumeCapability.GetMount(); mnt != nil {
			// This is a MOUNT_VOLUME request.
			fstype := mnt.GetFsType()
			if _, ok := s.supportedFilesystems[fstype]; !ok {
				// Zero capacity for unsupported filesystem type.
				response := &csi.GetCapacityResponse{AvailableCapacity: 0}
				return response, nil
			}
		}
	}
	layout, err := takeVolumeLayoutFromParameters(dupParams(request.GetParameters()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Invalid volume layout: err=%v", err)
	}
	bytesFree, err := s.volumeGroup.BytesFree(layout)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"Error in BytesFree: err=%v",
			err)
	}
	log.Printf("BytesFree: %v", bytesFree)
	defer s.reportStorageMetrics()
	response := &csi.GetCapacityResponse{AvailableCapacity: int64(bytesFree)}
	return response, nil
}

func (s *Server) ControllerGetCapabilities(
	ctx context.Context,
	request *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	capabilities := []*csi.ControllerServiceCapability{
		// CREATE_DELETE_VOLUME
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
				},
			},
		},
		// PUBLISH_UNPUBLISH_VOLUME
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
				},
			},
		},
		//
		//     Not supported by Controller service. This is
		//     performed by the Node service for the Logical
		//     Volume Service.
		//
		// LIST_VOLUMES
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
				},
			},
		},
		// GET_CAPACITY
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
				},
			},
		},
	}
	response := &csi.ControllerGetCapabilitiesResponse{Capabilities: capabilities}
	return response, nil
}

func (s *Server) CreateSnapshot(
	ctx context.Context,
	request *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	log.Printf("CreateSnapshot not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) DeleteSnapshot(
	ctx context.Context,
	request *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	log.Printf("DeleteSnapshot not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) ListSnapshots(
	ctx context.Context,
	request *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	log.Printf("ListSnapshots not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) ControllerExpandVolume(
	ctx context.Context,
	request *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	log.Printf("ControllerExpandVolume not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) ControllerGetVolume(
	ctx context.Context,
	request *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	log.Printf("ControllerGetVolume not supported")
	return nil, ErrCallNotImplemented
}

// NodeService RPCs

func (s *Server) NodeStageVolume(
	ctx context.Context,
	request *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	log.Printf("NodeStageVolume not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) NodeUnstageVolume(
	ctx context.Context,
	request *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	log.Printf("NodeUnstageVolume not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) NodeExpandVolume(
	ctx context.Context,
	request *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	log.Printf("NodeExpandVolume not supported")
	return nil, ErrCallNotImplemented
}

func (s *Server) NodeGetVolumeStats(
	ctx context.Context,
	request *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	log.Printf("NodeGetVolumeStats not supported")
	return nil, ErrCallNotImplemented
}

var ErrTargetPathNotEmpty = status.Error(
	codes.InvalidArgument,
	"Unexpected device already mounted at targetPath.")

var ErrTargetPathRO = status.Error(
	codes.InvalidArgument,
	"The targetPath is already mounted readonly.")

var ErrTargetPathRW = status.Error(
	codes.InvalidArgument,
	"The targetPath is already mounted read-write.")

func (s *Server) NodePublishVolume(
	ctx context.Context,
	request *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	pubcontext := request.GetPublishContext()
	sourcePath := ""
	if _, ok := pubcontext["datapath"]; !ok {
		return nil, status.Errorf(codes.Internal,"Missing 'datapath' in PubContxt: %v", pubcontext)
	}

	if pubcontext["datapath"] == "jbofis" {
		log.Printf("Logging into iSCSI Targets")
		targetlist, ok := pubcontext["targetlist"]
		if !ok {
			return nil, status.Errorf(codes.Internal,"Missing targetlist in PubContxt: %v", pubcontext)
		}
		targets := strings.Split(targetlist, ",")
		for _, target := range targets {
			chnks := strings.Split(target, "#")
			if len(chnks) == 3 {
				// Setup iscsi initiators for each drive
				blkdev, err := virsh.LoginIscsiTarget(chnks[0], chnks[2])
				if err != nil {
					//FIXME:  Need to clean up prior successful target setups
					return nil, status.Errorf(codes.Internal,"ISCSI Login Failes %v :: %v", chnks,err)
				}
				log.Printf("Volume path for %s is %v",chnks[0], blkdev)
			}
		}
		err := virsh.VgActivate(s.vgname)
		if err != nil {
			return nil, status.Errorf(codes.Internal,"FAILED to Find VG %s after ISCSI Login :: %v", s.vgname,err)
		}
	}

	id := request.GetVolumeId()
	if pubcontext["datapath"] == "direct" || pubcontext["datapath"] == "jbofis" {
		log.Printf("Looking up volume with id=%v", id)
		lv, err := s.volumeGroup.LookupLogicalVolume(id)
		if err != nil {
			return nil, ErrVolumeNotFound
		}
		log.Printf("Determining volume path")
		sourcePath, err = lv.Path()
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"Error in Path(): err=%v",
				err)
		}
		if err := lv.Activate(); err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"Failed to activate volume: err=%v",
				err)
		}
	}
	if pubcontext["datapath"] == "iscsi" {
		targetiqn, ok := pubcontext["blockid"]
		if !ok {
			return nil, status.Errorf(codes.Internal,"Missing 'blockid' in PubContxt: %v", pubcontext)
		}
		//FIXME - Assuming always Lun0 for now
		//lunstr, ok2 := pubcontext["lun"]
		//if !ok2 {
		//	return nil, status.Errorf(codes.Internal,"Missing 'lun' in PubContxt: %v", pubcontext)
		//}
		//lun, err := strconv.Atoi(lunstr)
		//if err != nil {
		//	return nil, status.Errorf(codes.Internal,"Unreadable lun number in PubContxt: %v", pubcontext)
		//}
		portal, ok3 := pubcontext["portal"]
		if !ok3 {
			return nil, status.Errorf(codes.Internal,"Missing 'portal' in PubContxt: %v", pubcontext)
		}

		// Setup iscsi initiator
		blkdev, err := virsh.LoginIscsiTarget(targetiqn, portal)
		if err != nil {
			return nil, status.Errorf(codes.Internal,"ISCSI Login Failes %v :: %v", pubcontext,err)
		}
		sourcePath = blkdev
	}


	log.Printf("Volume path is %v", sourcePath)
	targetPath := request.GetTargetPath()
	log.Printf("Target path is %v", targetPath)
	readonly := request.GetVolumeCapability().GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
	readonly = readonly || request.GetReadonly()
	log.Printf("Mounting readonly: %v", readonly)
	mountGroup := request.GetVolumeCapability().GetMount().GetVolumeMountGroup()
	allusrs, aok := pubcontext["allusers"]
	allusers :=  aok && strings.ToLower(allusrs) == "true"
	switch accessType := request.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		if virsh.ProxyMode() {
			return &csi.NodePublishVolumeResponse{}, virsh.MountVolume(sourcePath, targetPath, "block", mountGroup, "", readonly, allusers )
		}
		if err := s.nodePublishVolume_Block(sourcePath, targetPath, readonly); err != nil {
			return nil, err
		}
	case *csi.VolumeCapability_Mount:
		fstype := request.GetVolumeCapability().GetMount().GetFsType()
		mountOptions := request.GetVolumeCapability().GetMount().GetMountFlags()
		mountOptionsStr := strings.Join(mountOptions, ",")
		if virsh.ProxyMode() {
			return &csi.NodePublishVolumeResponse{}, virsh.MountVolume(sourcePath, targetPath, fstype, mountGroup, mountOptionsStr, readonly, allusers )
		}
		if err := s.nodePublishVolume_Mount(sourcePath, targetPath, readonly, fstype, mountOptions, mountGroup, allusers); err != nil {
			return nil, err
		}
	default:
		panic(fmt.Sprintf("lvm: unknown access_type: %+v", accessType))
	}

	// Set QOS
	iopspergb, ok := pubcontext["iopspergb"]
	if ok {
		mbpspergb, ok := pubcontext["mbpspergb"]
		if ok {
			lv, _ := s.volumeGroup.LookupLogicalVolume(id)
			err := lv.AddTag("qos-" + iopspergb + "-" + mbpspergb)
			if err != nil {
				log.Printf("ERROR setting QOS tag %+v \n", err)
			}
		}
	}

	response := &csi.NodePublishVolumeResponse{}
	return response, nil
}

func (s *Server) nodePublishVolume_Block(sourcePath, targetPath string, readonly bool) error {
	log.Printf("Attempting to publish volume %v as BLOCK_DEVICE to %v", sourcePath, targetPath)
	log.Printf("Determining mount info at %v", targetPath)
	// Check whether something is already mounted at targetPath.
	mp, err := getMountAt(targetPath)
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"Cannot get mount info at %v: err=%v",
			targetPath, err)
	}
	log.Printf("Mount info at %v: %+v", targetPath, mp)
	if mp != nil {
		// With lvm2, the sourcePath is typically a symlink to a
		// devicemapper device, for example:
		//   /dev/some-volume-group/some-logical-volume -> /dev/dm-4
		//
		// However, the mountpoint root shows the actual device, not
		// the symlink. As such, to determine whether or not the
		// device mounted at targetPath is the expected one, we need
		// to resolve the symlink and compare the targets.
		log.Printf("Following symlinks at %v", sourcePath)
		sourceDevicePath, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			return status.Errorf(
				codes.Internal,
				"Failed to follow symlinks at %v: err=%v",
				sourcePath, err)
		}
		log.Printf("Determined that %v -> %v", sourcePath, sourceDevicePath)
		// For bindmounts, we use the mountpoint root
		// in the current filesystem.
		mpdev := "/dev" + mp.root
		if mpdev != sourceDevicePath {
			return ErrTargetPathNotEmpty
		}
		log.Printf("The volume %v is already bind mounted to %v", sourcePath, targetPath)
		// For bind mounts, the filesystemtype and mount options are
		// ignored. As this RPC is idempotent, we respond with success.
		return nil
	} else {
		// The CSI Plug in is required to create the target
		log.Printf("Creating Mount Target  %v ", targetPath)
		if _, err := os.Create(targetPath); err != nil {
			return status.Errorf(
				codes.Internal,
				"Cannot create mount target %v: err=%v",
				targetPath, err)
		}
	}
	log.Printf("Nothing mounted at targetPath %v yet", targetPath)
	// Perform a bind mount of the raw block device. The
	// `filesystemtype` and `data` parameters to the
	// mount(2) system call are ignored in this case.
	flags := uintptr(syscall.MS_BIND)
	log.Printf("Performing bind mount of %s -> %s", sourcePath, targetPath)
	if err := syscall.Mount(sourcePath, targetPath, "", flags, ""); err != nil {
		_, ok := err.(syscall.Errno)
		if !ok {
			return status.Errorf(
				codes.Internal,
				"Failed to perform bind mount: err=%v",
				err)
		}
		return status.Errorf(
			codes.FailedPrecondition,
			"Failed to perform bind mount: err=%v",
			err)
	}
	return nil
}

func (s *Server) nodePublishVolume_Mount(sourcePath, targetPath string, readonly bool, fstype string, mountOptions []string, mountGroup string, allusers bool ) error {

	mountOptionsStr := strings.Join(mountOptions, ",")
	if virsh.ProxyMode() {
		return virsh.MountVolume(sourcePath, targetPath, fstype, mountGroup, mountOptionsStr, readonly, allusers )
	}

	log.Printf("Attempting to publish volume %v as MOUNT_DEVICE to %v", sourcePath, targetPath)
	var flags uintptr
	if readonly {
		flags |= syscall.MS_RDONLY
	}
	// Request validation ensures that the fstype is in our list of
	// supported filesystems.
	log.Printf("Requested filesystem type is '%v'", fstype)
	if fstype == "" {
		// If the fstype was not specified, pick the default.
		fstype = s.supportedFilesystems[""]
		log.Printf("No specific filesystem type requested, defaulting to %v", fstype)
	}
	// Check whether something is already mounted at targetPath.
	log.Printf("Determining mount info at %v", targetPath)
	mp, err := getMountAt(targetPath)
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"Cannot get mount info at %v: err=%v",
			targetPath, err)
	}
	log.Printf("Mount info at %v: %+v", targetPath, mp)
	if mp != nil {
		// For regular mounts, we use the mount source.
		if mp.mountsource != sourcePath {
			return ErrTargetPathNotEmpty
		}
		// Something is mounted at targetPath. We check that
		// the filesystem matches the requested one and that
		// the readonly status matches the requested readonly
		// status. If so, to support idempotency we return
		// success, otherwise we return an error as the
		// targetPath is not mounted in the requested way.
		if mp.fstype != fstype {
			return ErrMismatchedFilesystemType
		}
		if mp.isReadonly() != readonly {
			if mp.isReadonly() {
				return ErrTargetPathRO
			} else {
				return ErrTargetPathRW
			}
		}
		// The device, fstype and readonly option of
		// the filesystem at targetPath matches that
		// which is requested, to support idempotency
		// we return success.
		return nil
	} else {
		// CO SHALL be responsible for creating the directory
		// Creation of target_path is the responsibility of the SP.
		log.Printf("Checking Mount Target  %v ", targetPath)
		if _, err := os.Stat(targetPath); err != nil {
			log.Printf("Creating Mount Target  %v ", targetPath)
			if err := os.Mkdir(targetPath, 0770); err != nil {
				return status.Errorf(
					codes.Internal,
					"Cannot create mount target %v: err=%v",
					targetPath, err)
			}
		}
	}

	log.Printf("Determining filesystem type at %v", sourcePath)
	existingFstype, err := determineLocalFilesystemType(sourcePath)
	if err != nil {
		return status.Errorf(
			codes.Internal,
			"Cannot determine filesystem type: err=%v",
			err)
	}
	log.Printf("Existing filesystem type is '%v'", existingFstype)
	if existingFstype == "" {
		// There is no existing filesystem on the
		// device, format it with the requested
		// filesystem.
		log.Printf("The device %v has no existing filesystem, formatting with %v", sourcePath, fstype)
		if err := formatDevice(sourcePath, fstype); err != nil {
			return status.Errorf(
				codes.Internal,
				"formatDevice failed: err=%v",
				err)
		}
		existingFstype = fstype
	}
	if fstype != existingFstype {
		return ErrMismatchedFilesystemType
	}
	// Try to mount the volume by assuming it is correctly formatted.
	log.Printf("Mounting %v at %v fstype=%v, flags=%v mountOptions=%v", sourcePath, targetPath, fstype, flags, mountOptionsStr)
	if err := syscall.Mount(sourcePath, targetPath, fstype, flags, mountOptionsStr); err != nil {
		_, ok := err.(syscall.Errno)
		if !ok {
			return status.Errorf(
				codes.Internal,
				"Failed to perform mount: err=%v",
				err)
		}
		return status.Errorf(
			codes.FailedPrecondition,
			"Failed to perform mount: err=%v",
			err)
	}

	// Set Group ID on Mount target
	if mountGroup != "" {
		if gid, err := strconv.Atoi(mountGroup); err == nil {
			err := os.Chown(targetPath, -1, gid)
			if err != nil {
				fmt.Printf("WARNING MountGroup chown to %d failed. \n", gid)
			}
			_, err = exec.Command("chmod", "g+rwx", targetPath).CombinedOutput()
			if err != nil {
				log.Printf("ERROR setting g_rwx on %s \n%v\n", targetPath, err)
			}
		} else {
			fmt.Printf("WARNING MountGroup %s isn't a number.\n", mountGroup)
		}
	}

	// Open mount to all users.  Used for debugging or pods not match user and fsuser
	if allusers {
		_, err := exec.Command("chmod", "ugo+rwx", targetPath).CombinedOutput()
		if err != nil {
			log.Printf("ERROR setting ugo_rwx on %s \n%v\n", targetPath, err)
		}
	}

	return nil
}

func determineFilesystemType(devicePath string) (string, error) {
	if virsh.ProxyMode() {
		return virsh.FstypeProxy(devicePath)
	}
	return determineLocalFilesystemType(devicePath)
}
func determineLocalFilesystemType(devicePath string) (string, error) {
	// We use `file -bsL` to determine whether any filesystem type is detected.
	// If a filesystem is detected (ie., the output is not "data", we use
	// `blkid` to determine what the filesystem is. We use `blkid` as `file`
	// has inconvenient output.
	// We do *not* use `lsblk` as that requires udev to be up-to-date which
	// is often not the case when a device is erased using `dd`.
	output, err := exec.Command("file", "-bsL", devicePath).CombinedOutput()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(output)) == "data" {
		// No filesystem detected.
		return "", nil
	}
	// Some filesystem was detected, we use blkid to figure out what it is.
	output, err = exec.Command("blkid", "-c", "/dev/null", "-o", "export", devicePath).CombinedOutput()
	if err != nil {
		return "", err
	}
	parseErr := errors.New("Cannot parse output of blkid.")
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Split(strings.TrimSpace(line), "=")
		if len(fields) != 2 {
			return "", parseErr
		}
		if fields[0] == "TYPE" {
			return fields[1], nil
		}
	}
	return "", parseErr
}

func formatDevice(devicePath, fstype string) error {
	// scrub the first 256k of the device to head off any mkfs probe misfires.
	output, err := exec.Command(
		"dd", "if=/dev/zero", "of="+devicePath, "bs=512", "count=512", "conv=notrunc",
	).CombinedOutput()
	if err != nil {
		return errors.New("csilvm: formatDevice: dd failed: err=" + err.Error() + ": " + string(output))
	}
	output, err = exec.Command("mkfs", "-t", fstype, devicePath).CombinedOutput()
	if err != nil {
		return errors.New("csilvm: formatDevice: mkfs failed: err=" + err.Error() + ": " + string(output))
	}
	return nil
}

func (s *Server) NodeUnpublishVolume(
	ctx context.Context,
	request *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	id := request.GetVolumeId()
	targetPath := request.GetTargetPath()

	// We don't have a way to know at this point the data path of the mounted target.
	// First find the source of the mounted PVC.  
	mp, err := getMountAt(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Cannot get mount info at %v: err=%v",	targetPath, err)
	}

	response := &csi.NodeUnpublishVolumeResponse{}
	if mp == nil {
		// FIXME: If the targetPath doesn't exist there may be iscsi session that should be logged out.
		log.Printf("TargetPath not found %s", targetPath)
		return response, nil
	}
	var lv  *lvm.LogicalVolume
	switch strings.ToLower(mp.datapath) {
		case "iscsi":
			log.Printf("Unmounting iscsi device %+v", mp)
			err :=  virsh.UnMountVolume(targetPath,id)
			chunks := strings.SplitN(mp.blockpath, "-",4)
			log.Printf("CHUNKS %+v", chunks)
			if len(chunks) > 3{
				// Trim off lun-0 from end of path
				itarget := chunks[3][0:len(chunks[3])-6]
				err:= virsh.LogoutIscsiTarget(itarget,chunks[1])
				log.Printf("TARGET %s  PORTAL %s", itarget, chunks[1])
				if err != nil {
					log.Printf("ISCSI Logout failed %v", err)
				}
			}
			return response, err

		case "nvme":
			log.Printf("Unmounting nvme device %s", mp.blockpath)
			err :=  virsh.UnMountVolume(targetPath,id)
			return response, err

		case "qemu":
			log.Printf("Unmounting qemu device %s : %s", mp.blockpath,id)
			err :=  virsh.UnMountVolume(targetPath,id)
			return response, err

		case "sas":
			log.Printf("Unmounting SAS device %s : %s", mp.blockpath,id)
			//var err  error
			lv, err = s.volumeGroup.LookupLogicalVolume(id)
			// Clear QOS
			virsh.SetQos(lv.VgName(), lv.Name(), "0", "0")
			if virsh.ProxyMode() {
				err :=  virsh.UnMountVolume(targetPath,id)
				return response, err
			} else {
				// Unmount not containerized
				const umountFlags = 0
				log.Printf("Unmounting %v", targetPath)
				if err := syscall.Unmount(targetPath, umountFlags); err != nil {
					_, ok := err.(syscall.Errno)
					if !ok {
						return nil, status.Errorf(codes.Internal, "Failed to perform unmount: err=%v", err)
					}
					return nil, status.Errorf(
						codes.FailedPrecondition, "Failed to perform unmount: err=%v", err)
				}
			}
			if err := lv.Deactivate(); err != nil {
				log.Printf("Failed to de-activate volume: err=%v", err)
			}
			return response, nil

		default:
			log.Printf("Unmounting Unknown datapath device %s :: %v", mp.datapath,mp)
			const umountFlags = 0
			log.Printf("Unmounting %v target", targetPath)
			if err := syscall.Unmount(targetPath, umountFlags); err != nil {
				_, ok := err.(syscall.Errno)
				if !ok {
					return nil, status.Errorf(codes.Internal, "Failed to calling unmount: err=%v", err)
				}
				return nil, status.Errorf(codes.FailedPrecondition, "Failed to perform unmount: err=%v", err)
			}
			log.Printf("Deleting Target Path  %s", targetPath)
			os.RemoveAll(targetPath)
			return response, nil
	}
	// Can't Happen
	return nil, status.Errorf(codes.Internal,"ERROR with Unpublish handling")
}

func (s *Server) NodeGetInfo(
	ctx context.Context,
	request *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	tenant := s.vgname[5:len(s.vgname)]
	topology := &csi.Topology{
		Segments: map[string]string{tenant + topologyKey: s.nodeID},
	}

	// Valid iscsi IQN overrides nodeID
	initiatorName, err := readInitiatorName()
	if err == nil {
		return &csi.NodeGetInfoResponse{
			NodeId:             initiatorName,
			AccessibleTopology: topology,
		}, nil

	}

	return &csi.NodeGetInfoResponse{
		NodeId:             s.nodeID,
		AccessibleTopology: topology,
	}, nil
}

func zeroPartitionTable(devicePath string) error {
	// This method is the go equivalent of
	// `dd if=/dev/zero of=PhysicalVolume bs=512 count=1`.
	file, err := os.OpenFile(devicePath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(bytes.Repeat([]byte{0}, 512)); err != nil {
		return err
	}
	return nil
}

func statDevice(devicePath string) error {
	_, err := os.Stat(devicePath)
	return err
}

func calculatePVDiff(existing, pvnames []string) (missing, unexpected []string) {
	for _, epvname := range existing {
		had := false
		for _, pvname := range pvnames {
			if epvname == pvname {
				had = true
				break
			}
		}
		if !had {
			unexpected = append(unexpected, epvname)
		}
	}
	for _, pvname := range pvnames {
		had := false
		for _, epvname := range existing {
			if epvname == pvname {
				had = true
				break
			}
		}
		if !had {
			missing = append(missing, pvname)
		}
	}
	return missing, unexpected
}

func (s *Server) checkVolumeGroupTags(tags []string) error {
	if len(tags) != len(s.tags) {
		return fmt.Errorf("csilvm: Configured tags don't match existing tags: %v != %v", s.tags, tags)
	}
	for _, t1 := range tags {
		had := false
		for _, t2 := range s.tags {
			if t1 == t2 {
				had = true
				break
			}
		}
		if !had {
			return fmt.Errorf("csilvm: Configured tags don't match existing tags: %v != %v", s.tags, tags)
		}
	}
	return nil
}

func (s *Server) NodeGetCapabilities(
	ctx context.Context,
	request *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	var csc []*csi.NodeServiceCapability
	cl := []csi.NodeServiceCapability_RPC_Type{
		//TODO://csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_VOLUME_MOUNT_GROUP,
	}

	for _, cap := range cl {
		klog.V(4).Infof("enabled node service capability: %v", cap.String())
		csc = append(csc, &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: cap,
				},
			},
		})
	}

	return &csi.NodeGetCapabilitiesResponse{Capabilities: csc}, nil
}

// takeVolumeLayoutFromParameters removes and returns RAID-related parameters from the input.
func takeVolumeLayoutFromParameters(params map[string]string) (layout lvm.VolumeLayout, err error) {
	voltype, ok := params["type"]
	if ok {
		// Consume the 'type' key from the parameters.
		delete(params, "type")
		switch voltype {
		case "linear":
			layout.Type = lvm.VolumeTypeLinear
			strps, ok := params["stripes"]
			if ok {
				delete(params, "stripes")
				stripes, err := strconv.ParseUint(strps, 10, 64)
				if err != nil || stripes < 1 {
					return layout, fmt.Errorf("The 'stripes' parameter must be a positive integer: err=%v", err)
				}
				layout.Stripes = stripes
			}
		case "raid1":
			layout.Type = lvm.VolumeTypeRAID1
			smirrors, ok := params["mirrors"]
			if ok {
				delete(params, "mirrors")
				mirrors, err := strconv.ParseUint(smirrors, 10, 64)
				if err != nil || mirrors < 1 {
					return layout, fmt.Errorf("The 'mirrors' parameter must be a positive integer: err=%v", err)
				}
				layout.Mirrors = mirrors
			}
			nosync, okns := params["nosync"]
			if okns {
				delete(params, "nosync")
				if strings.ToLower(nosync) == "yes" || strings.ToLower(nosync) == "y" {
					layout.Nosync = 1
				}
			}
		case "raid5":
			layout.Type = lvm.VolumeTypeRAID5
			strps, ok := params["stripes"]
			if ok {
				delete(params, "stripes")
				stripes, err := strconv.ParseUint(strps, 10, 64)
				if err != nil || stripes < 1 {
					return layout, fmt.Errorf("The 'stripes' parameter must be a positive integer: err=%v", err)
				}
				layout.Stripes = stripes
			}
		case "raid6":
			layout.Type = lvm.VolumeTypeRAID6
			strps, ok := params["stripes"]
			if ok {
				delete(params, "stripes")
				stripes, err := strconv.ParseUint(strps, 10, 64)
				if err != nil || stripes < 1 {
					return layout, fmt.Errorf("The 'stripes' parameter must be a positive integer: err=%v", err)
				}
				layout.Stripes = stripes
			}
		case "raid10":
			layout.Type = lvm.VolumeTypeRAID10
			strps, ok := params["stripes"]
			if ok {
				delete(params, "stripes")
				stripes, err := strconv.ParseUint(strps, 10, 64)
				if err != nil || stripes < 1 {
					return layout, fmt.Errorf("The 'stripes' parameter must be a positive integer: err=%v", err)
				}
				layout.Stripes = stripes
			}
			nosync, okns := params["nosync"]
			if okns {
				delete(params, "nosync")
				if strings.ToLower(nosync) == "yes" || strings.ToLower(nosync) == "y" {
					layout.Nosync = 1
				}
			}
		default:
			return layout, errors.New("The 'type' parameter must be one of 'linear', 'raid1', 'raid5', 'raid6' or 'raid10'.")
		}
	}
	return layout, nil
}

func dupParams(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	params := make(map[string]string, len(in))
	for k, v := range in {
		params[k] = v
	}
	return params
}



// volumeOptsFromParameters parses volume create parameters into
// lvm.CreateLogicalVolumeOpt funcs.  If returns an error if there are
// unconsumed parameters or if validation fails.
func volumeOptsFromParameters(in map[string]string) (opts []lvm.CreateLogicalVolumeOpt, err error) {
	// Create a duplicate map so we don't mutate the input.
	params := dupParams(in)
	// Transform any 'type' parameter into an opt.
	layout, err := takeVolumeLayoutFromParameters(params)
	if err != nil {
		return nil, err
	}
	opts = append(opts, lvm.VolumeLayoutOpt(layout))
	// Ignore Datapath volume parameters.
	_, ok := params["datapath"]
	if ok {
		delete(params, "datapath")
	}

	_, ok = params["stolakejobfurls"]
	if ok {
		delete(params, "stolakejobfurls")
	}

	// Ignore QOS settings
	_, ok = params["iopspergb"]
	if ok {
		delete(params, "iopspergb")
	}
	_, ok = params["mbpspergb"]
	if ok {
		delete(params, "mbpspergb")
	}
	if len(params) > 0 {
		var keys []string
		for k := range params {
			keys = append(keys, k)
		}
		return nil, fmt.Errorf("Unexpected parameters: %v", keys)
	}
	return opts, nil
}

// Serialize all requests. This avoids issues observed when deleting 80 logical
// volumes in parallel where calls to `lvs` appear to hang.
//
// See https://jira.mesosphere.com/browse/DCOS_OSS-4642
func SerializingInterceptor() grpc.UnaryServerInterceptor {
	// Instead of a mutex, use a weighted semaphore because it's sensitive to context cancellation and/or deadline
	// expiration, which is important for maintaining a healthy request queue, and also helps prevent execution of
	// operations that the calling CO is no longer interested in.
	sem := semaphore.NewWeighted(1)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		err := sem.Acquire(ctx, 1)
		if err != nil {
			return nil, err
		}
		// Acquire can still succeed if the context is canceled, double-check it.
		select {
		case <-ctx.Done():
			sem.Release(1)
			return nil, ctx.Err()
		default:
		}
		defer sem.Release(1)
		return handler(ctx, req)
	}
}

// RequestLimitInterceptor limits the number of pending requests in flight at any given time. If an incoming request
// would exceed the specified requestLimit then an Unavailable gRPC error is returned.
func RequestLimitInterceptor(requestLimit int) grpc.UnaryServerInterceptor {
	sem := semaphore.NewWeighted(int64(requestLimit))
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !sem.TryAcquire(1) {
			return nil, status.Error(codes.Unavailable, "Too many pending requests. Please retry later.")
		}
		defer sem.Release(1)
		return handler(ctx, req)
	}
}


// readInitiatorName: Extract the initiator name from /etc/iscsi file
func readInitiatorName() (string, error) {
	initiatorNameFilePath := "/etc/iscsi/initiatorname.iscsi"
	file, err := os.Open(initiatorNameFilePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if equal := strings.Index(line, "="); equal >= 0 {
			if strings.TrimSpace(line[:equal]) == "InitiatorName" {
				return strings.TrimSpace(line[equal+1:]), nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", fmt.Errorf("InitiatorName key is missing from %s", initiatorNameFilePath)
}

