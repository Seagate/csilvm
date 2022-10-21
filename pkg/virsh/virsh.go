// Copyright (C) 2021 Seagate Technology LLC and/or its Affiliates.
// SPDX-License-Identifier: LGPL-2.1-only

// These functions provide a LVM2 and virsh control through a mercury proxy
// running on the ovirt host or Red Hat Virtualization host
// See: https://linux.die.net/man/1/virsh

package virsh

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	//"encoding/json"
	"encoding/xml"
	"errors"
	"io/ioutil"

	pb  "github.com/Seagate/csiclvm/pkg/stolake"
	//pb  "seagit.okla.seagate.com/tyt-speedboat/stolake/proto"

	"strings"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
)

// Global variable for URL to StoLake agent
var StolakeURL string
var HostIP string // FIXME  Removing Mecury access

var logger logr.Logger

type basicError string

func (s basicError) Error() string { return string(s) }

const ErrDomNotFound = basicError("virsh: domain not found")

// virsh Pool Dump Struct
type Pool struct {
	XMLName  xml.Name `xml:"pool"`
	Text     string   `xml:",chardata"`
	Type     string   `xml:"type,attr"`
	Name     string   `xml:"name"`
	Uuid     string   `xml:"uuid"`
	Capacity struct {
		Text string `xml:",chardata"`
		Unit string `xml:"unit,attr"`
	} `xml:"capacity"`
	Allocation struct {
		Text string `xml:",chardata"`
		Unit string `xml:"unit,attr"`
	} `xml:"allocation"`
	Available struct {
		Text string `xml:",chardata"`
		Unit string `xml:"unit,attr"`
	} `xml:"available"`
	Source struct {
		Text   string `xml:",chardata"`
		Name   string `xml:"name"`
		Format struct {
			Text string `xml:",chardata"`
			Type string `xml:"type,attr"`
		} `xml:"format"`
	} `xml:"source"`
	Target struct {
		Text string `xml:",chardata"`
		Path string `xml:"path"`
	} `xml:"target"`
}

type vmblkmap struct {
	vmname string
	vdblk  string
	lvsrc  string
}

type mercinfo struct {
	Api        string `json:"api"`
	Cluster    string `json:"cluster"`
	Registered string `json:"registered"`
	Seachest   string `json:"seachest"`
	Server     string `json:"server"`
	Version    string `json:"version"`
}

type Stolakeclient struct {
	Client     pb.StolakeClient
	ClientConn *grpc.ClientConn
	Log        logr.Logger
} // stolakeclient

const TIMEOUT = 6 * time.Second //gRPC Timeout Call limit

func ProxyStoLakeRun(cmd string, args ...string) ([]byte, error) {
	//CSICheck()
        sc, connErr := connect()
        if connErr != nil {
		return nil , connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.MercProxyReq{
		Cmd:  cmd,
		Args: args,
	}
	res, err := sc.Client.MercuryProxy(ctx, req)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
	}
	log.Printf("STOLAKEPROXY: %s %v  \n", cmd, args)
	//log.Printf("STOLAKEPROXY: %s %v RESULT: %s \n", cmd, args, res)
	return []byte(res.GetStdout()), err
}




func SetStolakeURL(urlstr string) bool {
	_, err := url.Parse(urlstr)
	if err != nil {
		StolakeURL = ""
		log.Printf("FAILED StoLake URL Invalid %s \n", urlstr)
		return false
	}
	_, err = os.Stat(urlstr)
	if err != nil {
		log.Printf("FAILED StoLake URL %s does not exist \n", urlstr)
		return false
	}
	StolakeURL = urlstr
	return true
}

func GetProxyURL() string {
	return StolakeURL
}

func ProxyMode() bool {
	if StolakeURL == "none" {
		return false
	}
	if StolakeURL == "" {
		return false
	}
	return true
}

// Function makes LVM call through Mercury Agent on oVrit Host
func ProxyRun(cmd string, args ...string) ([]byte, error) {
	runargs := ""
	for _, arg := range args {
		// Assume tilda is a save delimeter
		runargs += arg + "~"
	}
	runargs = strings.TrimSuffix(runargs, "~")
	url := "http://" + HostIP + ":3141/speedboat/lvm/run?lvmcmd=" + cmd + "&lvmargs=" + runargs
	log.Printf("GET: %s\n", url)
	//resp, err := http.Get("http://" + HostIP + ":3141/speedboat/virsh/run?lvmcmd=" + cmd + "&lvmargs=" + runargs)
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
		return nil, err
	}
	jbytes, err2 := ioutil.ReadAll(resp.Body)
	if err2 != nil {
		return nil, err2
	}
	return jbytes, nil
}

// OLD Code 
// FIXME Add VM support ot StoLake and rewrite these functions
// Test if virtual machine exists
func IsDomValid(dom string) bool {
	url := "http://" + HostIP + ":3141/speedboat/virsh/run?args=domid~" + dom
	log.Printf("GET: %s\n", url)
	get, err := http.Get(url)
	defer get.Body.Close()
	if err != nil {
		log.Printf("VMLOOKUP FAILED: %+v", err)
		return false
	}
	if get.StatusCode != 200 {
		log.Printf("VMLOOKUP FAILED: %+v", get)
		return false
	}
	return true
}

// Test if storage pool exists
func IsPoolValid(pool string) bool {
	url := "http://" + HostIP + ":3141/speedboat/virsh/run?args=pool-uuid~" + pool
	get, err := http.Get(url)
	defer get.Body.Close()
	if err != nil {
		log.Printf("GET: %s\n", url)
		log.Printf("VMPool Lookup faile: %+v", err)
		return false
	}
	if get.StatusCode != 200 {
		log.Printf("VMPool Lookup faile: %+v", get)
		return false
	}
	return true
}

//List Virtual Machines
func ListVMs() (vms []string) {
	args := []string{"list"}
	out, _ := virshProxy(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words) < 3 {
			continue
		}
		if words[0] == "Id" {
			continue
		}
		vms = append(vms, words[1])
	}
	fmt.Printf("VMS %v\n", vms)
	return vms
}

//List VM's Speedboat Blk Devices
func ListVMblks(vmname string) (mappings []vmblkmap) {
	args := []string{"domblklist", vmname}
	out, _ := virshProxy(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words) < 2 {
			continue
		}
		if words[0] == "Target" {
			continue
		}
		// Only list Speedboat VGs
		if len(words[1]) < 11 {
			continue
		}
		if words[1][0:10] != "/dev/sbvg_" {
			continue
		}
		var blkmap vmblkmap
		blkmap.vmname = vmname
		blkmap.vdblk = words[0]
		blkmap.lvsrc = words[1]
		mappings = append(mappings, blkmap)
	}
	//fmt.Printf("MAPPING For %s  %v\n", vmname, mappings)
	return mappings
}

//List Virsh Pools
func ListMappings() (mappings []vmblkmap) {
	vms := ListVMs()
	for _, vm := range vms {
		vmblkmap := ListVMblks(vm)
		mappings = append(mappings, vmblkmap...)
	}
	return mappings
}

func virshProxy(args []string) ([]byte, error) {
	runargs := ""
	for _, arg := range args {
		// Assume tilda is a save delimeter
		runargs += arg + "~"
	}
	runargs = strings.TrimSuffix(runargs, "~")
	url := "http://" + HostIP + ":3141/speedboat/virsh/run?args=" + runargs
	log.Printf("GET: %s\n", url)
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
		return nil, err
	}
	bytes, err2 := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err2 != nil {
		return nil, err2
	}
	return bytes, nil
}

func FstypeProxy(devicepath string) (string, error) {
        sc, connErr := connect()
        if connErr != nil {
		return "" , connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.FileSystemTypeReq{
		DevPath:  devicepath,
	}
	res, err := sc.Client.FileSystemType(ctx, req)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
		log.Printf("STOLAKEPROXY RESULT: %v \n", res)
	}
	return string(res.GetFsType()), err
}


func MountInfo() ([]byte, error) {
	//CSICheck()
        sc, connErr := connect()
        if connErr != nil {
		return nil , connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.MountInfoReq{}
	res, err := sc.Client.MountInfo(ctx, req)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
	}
	return []byte(res.GetInfo()), err
}


func MountVolume(source, target, fstype, guid, mountoptions string, readonly, allusers bool)  error {
        sc, connErr := connect()
        if connErr != nil {
		return connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.MountVolumeReq{
		SourcePath:  source,
		TargetPath:  target,
		FsType:  fstype,
		ReadOnly:  readonly,
		MountOptions:  mountoptions,
		GroupId:  guid,
		AllUsers:  allusers,
	}
	res, err := sc.Client.MountVolume(ctx, req)
	log.Printf("STOLAKEPROXY RESULT: %v \n", res)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
	}
	return err
}

func UnMountVolume(target, volumeid string)  error {
        sc, connErr := connect()
        if connErr != nil {
                log.Printf("Failed to connect to Server \n%v\n",connErr)
		return connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.UnMountVolumeReq{
		TargetPath:  target,
	}
	res, err := sc.Client.UnMountVolume(ctx, req)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
		log.Printf("STOLAKEPROXY RESULT: %v \n", res)
	}
	return err
}

//List Virsh Pools
func ListAllPools() (pools []string) {
	args := []string{"pool-list", "--all"}
	out, _ := virshProxy(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words) < 2 {
			continue
		}
		if words[0] == "Name" {
			continue
		}
		if len(words[0]) < 5 {
			continue
		}
		if words[0][0:5] != "sbvg_" {
			continue
		}
		pools = append(pools, words[0])
	}
	//fmt.Printf("POOLS %v\n", pools)
	return pools
}

//List Virsh Pools
func ListPools() (pools []string) {
	args := []string{"pool-list"}
	out, _ := virshProxy(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words) < 2 {
			continue
		}
		if words[0] == "Name" {
			continue
		}
		if len(words[0]) < 5 {
			continue
		}
		if words[0][0:5] != "sbvg_" {
			continue
		}
		pools = append(pools, words[0])
	}
	//fmt.Printf("POOLS %v\n", pools)
	return pools
}

//List Virsh Volumes on Hypervisor
func ListHyprVols(pool string) map[string]string {
	vols := make(map[string]string)
	args := []string{"vol-list", pool}
	out, _ := virshProxy(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words) < 2 {
			continue
		}
		if words[0] == "Name" {
			continue
		}
		if len(words[0]) < 2 {
			continue
		}
		vols[words[0]] = words[1]
	}
	return vols
}

func VolPath(pool, vol string) (string, error) {
	args := []string{"vol-path", "--pool", pool, vol}
	out, err := virshProxy(args)
	return string(out), err
}

func DisplayVols(vols map[string]string) {
	for name, _ := range vols {
		//fmt.Printf(" >  %s %s  \n", name,path)
		fmt.Printf(" >  %s\n", name)
	}
}

func NextOpenVdx(vm string) (string, error) {
	mappedblks := ListVMblks(vm)
	next := "vd"
	for _, c := range "abcdefghijklmnopqrstuvwxyz" {
		found := false
		for _, blkmap := range mappedblks {
			if "vd"+string(c) == blkmap.vdblk {
				found = true
				break
			}
		}
		if !found {
			next += string(c)
			break
		}
	}
	if next == "vd" {
		return "vdxerror", errors.New("Can't find an open vdx block handle on VM")
	}
	return next, nil
}

//Attache device from VM
func AttachDisk(vm, vgroup, lvname string) (string, error) {
	url := "http://" + HostIP + ":3141/speedboat/virsh/attachdisk?nodeid=" + vm
	url += "&vgroup=" + vgroup
	url += "&lvname=" + lvname

	log.Printf("GET: %s\n", url)
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
		return "", err
	}
	bytes, err2 := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err2 != nil {
		return "", err2
	}
	return string(bytes), nil
}

//Detach disk from VM
func DetachDisk(vm, vgroup, lvname string) error {
	url := "http://" + HostIP + ":3141/speedboat/virsh/detachdisk?nodeid=" + vm
	url += "&vgroup=" + vgroup
	url += "&lvname=" + lvname

	log.Printf("GET: %s\n", url)
	resp, err := http.Get(url)
	defer resp.Body.Close()
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
		return err
	}
	return nil
}

func ListVGs() (vgs []string) {
	//FIXME
	vgs = []string{"sbvg_datalake"}
	return vgs
}

func SetQos(vgname, lvname, iopspergb, mbpspergb string) error {
	targetPath := "/dev/" + vgname + "/" + lvname

        sc, connErr := connect()
        if connErr != nil {
		return connErr
        }

        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.LvQoSReq {
		TargetPath: targetPath,
		IOPSperGB:  iopspergb,
		MBpSperGB:  mbpspergb,
	}
	res, err := sc.Client.LvQoS(ctx, req)
        defer sc.ClientConn.Close()
	if err != nil {
		log.Print(err.Error())
		log.Printf("SET QOS Failed: %v \n", res)
	}
	return err
}

func StageIscsiTarget(lvuuid, initiqn string) (targetiqn, lun, portal string, err error) {
        sc, connErr := connect()
        if connErr != nil {
		return "", "", "", connErr
        }
        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.StageIscsiReq {
		LvUuid: lvuuid,
		InitiatorIqn:  initiqn,
	}
	res, err := sc.Client.StageIscsi(ctx, req)
        defer sc.ClientConn.Close()
	targetiqn = string(res.GetTargetIqn())
	lun = string(res.GetLun())
	// Remove 'lun' text if present
	lun = strings.Replace(lun, "lun", "", -1)

	portal = string(res.GetTargetPortal())
	// If Portal = 0.0.0.0 (listening on all) use node IP passed 
	// as an environment variable by pod spec
	if portal[0:7] == "0.0.0.0" {
		if os.Getenv("CSI_NODE_IP") != "" {
			portal = os.Getenv("CSI_NODE_IP") + portal[7:len(portal)]
		} else {
			log.Printf("WARNING: CSI_NODE_IP environment variable not set for Portal IP subsitution")
		}
	}

	return targetiqn, lun, portal, err
}

func UnStageIscsiTarget(lvuuid, initiqn string) error {
        sc, connErr := connect()
        if connErr != nil {
		return connErr
        }
        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
	req := &pb.UnStageIscsiReq {
		LvUuid: lvuuid,
		InitiatorIqn:  initiqn,
	}
	_, err := sc.Client.UnStageIscsi(ctx, req)
        defer sc.ClientConn.Close()
	return err
}


func IscsiTargetExists(targetiqn, portal string)  bool {
	var args []string
	args = append(args, "-m", "node")
	args = append(args, "--target", targetiqn)
	args = append(args, "--portal", portal)
	res, err := ProxyStoLakeRun("iscsiadm", args...)
	if err != nil {
		log.Printf("ISCSADM LIST ERROR: %v : %v", args, err)
	}
	if len(res) < 100 {
		return false
	}
	return  true

}

// Login to iscsi target and return the block device handle
func LoginIscsiTarget(targetiqn, portal string) ( string, error) {
	// First OS needs to discovery targets at portal
	args := []string{ "-m", "discoverydb", "--type","sendtargets","--discover"}
	args = append(args, "--portal", portal)
	_, err := ProxyStoLakeRun("iscsiadm", args...)
	if err != nil {
		return "",  fmt.Errorf("ISCSADM ERROR: %v : %v", args, err)
	}
	// Next log in to target
	args = []string{ "-m", "node", "--login"}
	args = append(args, "--target", targetiqn)
	args = append(args, "--portal", portal)
	_, err = ProxyStoLakeRun("iscsiadm", args...)
	if err != nil {
		return "", fmt.Errorf("ISCSADM ERROR: %v : %v", args, err)
	}
	scsidevs, err2 := LsSscsiTransports()
	if err != nil {
		return "", err2
	}
	for _, scsidev := range scsidevs {
		if scsidev.transport == targetiqn {
			return scsidev.blkdev, nil
		}
	}
	return "", fmt.Errorf("ISCSI Block Device not found: %v ", scsidevs)
}

func LogoutIscsiTarget(targetiqn, portal string)  error {
	var args []string
	args = append(args, "-m", "node", "--logout")
	args = append(args, "--target", targetiqn)
	//args = append(args, "--portal", portal)
	res, err := ProxyStoLakeRun("iscsiadm", args...)
	if err != nil {
		log.Printf("ERROR: from Proxy :: %v -> %v \n",args, err)
	}
	log.Printf("DEBUG: iscsiadm  %v -> %s \n",args, res)
	// delete discovery record
	args = []string{"-m", "node", "-o", "delete"}
	args = append(args, "--target", targetiqn)
	res, err = ProxyStoLakeRun("iscsiadm", args...)
	if err != nil {
		log.Printf("WARNING: iscsiadm delete Failed:: %v -> %s \n",args, err)
	}
	log.Printf("DEBUG: iscsiadm  %v -> %s \n",args, res)
	return  nil
}


func LsSscsiTransports() ([]lsscsi, error ) {
	args := []string{"-t"}
	res, err := ProxyStoLakeRun("lsscsi", args...)
	if err != nil {
		log.Printf("LSSCSI  ERROR: %v ",  err)
	}
	scsidevs, err2 := parseLsScsi(res)
	if err2 != nil {
		log.Printf("LSSCSI PARSE ERROR: %v ",  err)
	}
	return scsidevs, nil
}

type lsscsi struct {
	hctl       string
	kind       string
	transport  string
	blkdev     string
}


func parseLsScsi(buf []byte) (devs []lsscsi, err error) {
	for _, line := range strings.Split(string(buf), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, errors.New("Failed to parse lsscsi")
		}
		chnks := strings.Split(fields[2], ",")
		dev := lsscsi{
			hctl:        fields[0],
			kind:        fields[1],
			transport:   chnks[0],
			blkdev:      fields[3],
		}
		devs = append(devs, dev)
	}
	return devs, nil
}




// FIXME:  Add Sycn call to StoLake
func QosSync() error {
	url := "http://localhost:3141/speedboat/claims/qossync"
	log.Printf("GET: %s\n", url)
	resp, err := http.Get(url)
	defer resp.Body.Close()
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
	}
	return err
}

func StartAllPools() {
	vgs := ListVGs()
	for _, vg := range vgs {
		DefinePool(vg)
		StartPool(vg)
		vols := ListHyprVols(vg)
		DisplayVols(vols)
	}
}

func DefinePool(vg string) error {
	args := []string{"pool-define-as", vg, "logical", "--source-name", vg, "--target", "/dev/" + vg}
	_, err := virshProxy(args)
	return err
}

func StartPool(vg string) error {
	pools := ListPools()
	for _, pool := range pools {
		if vg == pool {
			return nil
		}
	}
	args := []string{"pool-start", vg}
	_, err := virshProxy(args)
	return err
}

func UndefinePool(vg string) {
	args := []string{"pool-destroy", vg}
	virshProxy(args)
	args = []string{"pool-undefine", vg}
	virshProxy(args)
}

func BlkID(blkdev string) (string, error) {
	cmd := exec.Command("blkid", "-po", "udev", blkdev)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.New("Can't find block device on host")
	}

	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		chunks := strings.Split(ln, "=")
		if len(chunks) == 2 {
			if chunks[0] == "ID_FS_UUID" {
				return chunks[1], nil
			}
		}
	}
	return "", errors.New("Can't find blockid device on host")
}


func connect() (*Stolakeclient, error) {
        // Set up a connection to the server.
        conn, err := grpc.Dial("unix://"+StolakeURL, grpc.WithInsecure(), grpc.WithBlock())
        if err != nil {
                logger.Error(err, "Failed to connect to Server")
                log.Printf("Failed to connect to Server \n%v\n",err)
        }
        c := pb.NewStolakeClient(conn)

        sc := new(Stolakeclient)
        sc = &Stolakeclient{
                Client:     c,
                ClientConn: conn,
        }

        return sc, err
}

func disconnect(sc *Stolakeclient) error {
        err := sc.ClientConn.Close()
        return err
}



func CSICheck() {
        cli, connErr := connect()
        if connErr != nil {
                log.Printf("Failed to connect to Server \n%v\n",connErr)
        }


        ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
        defer cancel()
        req := &pb.GetInfoReq{}

        res, err := cli.Client.RetrieveInfo(ctx, req)
        if err != nil {
                log.Printf("RetrieveInfo call Failed \n %v",err)
        } else {
                log.Printf(fmt.Sprintf("AGENT INFO: %+v", res))
        }


        disConnErr := disconnect(cli)
        if disConnErr != nil {
                log.Printf("Disconnect from Server Failed\n%v\n",disConnErr)
        } else {
                log.Printf("Disconnected from gRPC Server !!")
        }
}





func main() {
	pools := ListAllPools()
	fmt.Printf("POOLS %v\n", pools)
	mappings := ListMappings()
	fmt.Printf("MAPPINGS %v\n", mappings)
	//DetachDisk("Cent7","vdc")

	vgs := ListVGs()
	fmt.Printf("VGS %v\n", vgs)
	StartAllPools()

	//err:= AttachDisk("Cent7","/dev/sbvg_datalake/topper" )
	//err:= AttachDisk("Cent7","/dev/sbvg_datalake/idle4" )
	//if err != nil {	fmt.Printf("ERROR ATTACHING  %+v\n",err) }
}

//List speedboat VGs
// AllVolumeGroups

//Create virsh pool from VG
//Is VG a Pool
