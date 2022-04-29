// Copyright (C) 2021 Seagate Technology LLC and/or its Affiliates.
// SPDX-License-Identifier: LGPL-2.1-only

// These functions provide a LVM2 and virsh control through a mercury proxy
// running on the ovirt host or Red Hat Virtualization host
// See: https://linux.die.net/man/1/virsh

package virsh

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	//"log"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io/ioutil"
	"log"

	pb "github.com/Seagate/csiclvm/pkg/proto"
	//pb  "seagit.okla.seagate.com/tyt-speedboat/stolake/proto"

	"strings"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
)

// Global variable for URL to StoLake agent
var ProxyURL string
var HostIP string // FIXME  Removing Mecury access

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

const TIMEOUT = 60 * time.Second //gRPC Timeout Call limit
func callMercProxy(sc *Stolakeclient, cmd string, args []string) (*pb.MercProxyRes, error) {
	ctx, cancel := context.WithTimeout(context.Background(), TIMEOUT)
	defer cancel()
	req := &pb.MercProxyReq{
		Cmd:  cmd,
		Args: args,
	}
	r, err := sc.Client.MercuryProxy(ctx, req)
	if err != nil {
		log.Print(err.Error())
	}
	return r, err
}

func HostConfig(ip string) (mercinfo, error) {
	var info mercinfo
	resp, err := http.Get("http://" + ip + ":3141/")
	if err != nil {
		return info, err
	}
	jbytes, err2 := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err2 != nil {
		return info, err2
	}
	json.Unmarshal(jbytes, &info)
	return info, nil
}

// Function makes LVM call through Mercury Agent on oVrit Host
func ProxyRun(cmd string, args ...string) ([]byte, error) {
	log.Printf("PROXYURL: %s %s \n", ProxyURL, ProxyURL[0:5])
	if ProxyURL[0:5] == "unix:" {
		log.Printf("PROXY: %s %v \n", cmd, args)
		conn, err := grpc.Dial(ProxyURL, grpc.WithInsecure(), grpc.WithBlock())
		if err != nil {
			log.Fatalf("did not connect: %v", err)
		}
		defer conn.Close()
		c := pb.NewStolakeClient(conn)
		sc := &Stolakeclient{
			Client:     c,
			ClientConn: conn,
		}
		res, err := callMercProxy(sc, cmd, args)
		return []byte(res.GetStdout()), err
	}
	//FIXME DEBUG Exit
	return nil, nil

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
	defer resp.Body.Close()
	if err2 != nil {
		return nil, err2
	}
	return jbytes, nil
}

func SetProxyURL(urlstr string) bool {
	_, err := url.Parse(urlstr)
	if err != nil {
		ProxyURL = ""
		return false
	}
	ProxyURL = urlstr
	return true
}

func GetProxyURL() string {
	return ProxyURL
}

func ProxyMode() bool {
	if ProxyURL == "" {
		return false
	}
	return true
}

// FIXME Not Used yet
type vginfo struct {
	FreeBytes string `json:"FreeBytes"`
	LVCount   string `json:"LVCount"`
	PartCount string `json:"PartCount"`
	UsedBytes string `json:"UsedBytes"`
	CsiStatus string `json:"csistatus"`
	VgLock    string `json:"vglock"`
	vgroup    string `json:"vgroup"`
}

func LookupVolumeGroup(vg, ip string) (string, error) {
	if vg[0:5] != "sbvg_" {
		return "", fmt.Errorf("Not Valid Speedboat Volume Group %s\n", vg)
	}
	get, err := http.Get("http://" + ip + ":3141/speedboat/tenant/join?tenantname=" + vg[5:])
	if err != nil {
		return "", err
	}
	defer get.Body.Close()
	//log.Printf("GET: %+v", get)
	if get.StatusCode != 200 {
		return "", fmt.Errorf("Tenant Join Failed")
	}

	jbytes, err2 := ioutil.ReadAll(get.Body)
	defer get.Body.Close()
	if err2 != nil {
		return "", err2
	}
	log.Printf("%s", jbytes)
	//found.name =  vg
	return vg, nil
}

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
	url := "http://" + HostIP + ":3141/speedboat/claims/fstype?devpath=" + devicepath
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
	url := "http://localhost:3141/speedboat/claims/qos?tenantname=" + vgname[5:]
	url += "&claim=" + lvname
	url += "&iopspergb=" + iopspergb
	url += "&mbpspergb=" + mbpspergb

	log.Printf("GET: %s\n", url)
	resp, err := http.Get(url)
	defer resp.Body.Close()
	if err != nil {
		log.Printf("GETERROR: %s\n%v\n", url, err)
	}
	return err
}

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
