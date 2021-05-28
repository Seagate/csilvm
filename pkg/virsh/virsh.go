// Copyright (C) 2021 Seagate Technology LLC and/or its Affiliates.
// SPDX-License-Identifier: LGPL-2.1-only

// This file abstracts the virsh operations of oVirt and Red Hat Virtualization
// See: https://linux.die.net/man/1/virsh


package virsh

import (
    "os/exec"
    "fmt"
    //"log"
    "encoding/xml"
    "strings"
    "errors"
    "os"
    "io/ioutil"
)

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
	vmname	string
	vdblk	string
	lvsrc	string
}


// Test if virtual machine exists
func IsDomValid(dom string) bool {
	cmd := exec.Command("virsh", "domid", dom)
        _, err := cmd.CombinedOutput()
	if (err == nil){
		return true
	}
	return false
}

// Test if storage pool exists
func IsPoolValid(pool string) bool {
	cmd := exec.Command("virsh", "pool-uuid", pool)
        _, err := cmd.CombinedOutput()
	if (err == nil){
		return true
	}
	return false
}



//List Virtual Machines
func ListVMs() (vms []string) {
	args := []string{"list"}
	out := virshcmd(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words)<3  { continue }
		if words[0] == "Id" { continue }
		vms = append(vms, words[1])
	}
	fmt.Printf("VMS %v\n", vms)
	return vms
}


//List VM's Speedboat Blk Devices
func ListVMblks(vmname string) (mappings []vmblkmap) {
	args := []string{"domblklist", vmname}
	out := virshcmd(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words)<2  { continue }
		if words[0] == "Target" { continue }
		// Only list Speedboat VGs
		if len(words[1])<11  { continue }
		if (words[1][0:10] != "/dev/sbvg_")  { continue }
		var blkmap vmblkmap
		blkmap.vmname = vmname
		blkmap.vdblk = words[0]
		blkmap.lvsrc = words[1]
		mappings = append(mappings,blkmap)
	}
	//fmt.Printf("MAPPING For %s  %v\n", vmname, mappings)
	return mappings
}


//List Virsh Pools
func ListMappings() (mappings []vmblkmap) {
	vms := ListVMs()
	for _,vm := range vms {
		vmblkmap := ListVMblks(vm)
		mappings = append(mappings, vmblkmap...)
	}
	return mappings
}



func virshcmd(args []string) ([]byte) {
	cmdb := "virsh"
	cmd := exec.Command(cmdb, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("INFO Error running  %+v\n %+v\n",cmd,err)
	}
	return out
}

//List Virsh Pools
func ListAllPools() (pools []string) {
	args := []string{"pool-list","--all"}
	out := virshcmd(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words)<2  { continue }
		if words[0] == "Name" { continue }
		if len(words[0])<5  { continue }
		if (words[0][0:5] != "sbvg_")  { continue }
		pools = append(pools, words[0])
	}
	//fmt.Printf("POOLS %v\n", pools)
	return pools
}


//List Virsh Pools
func ListPools() (pools []string) {
	args := []string{"pool-list"}
	out := virshcmd(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words)<2  { continue }
		if words[0] == "Name" { continue }
		if len(words[0])<5  { continue }
		if (words[0][0:5] != "sbvg_")  { continue }
		pools = append(pools, words[0])
	}
	//fmt.Printf("POOLS %v\n", pools)
	return pools
}


//List Virsh Volumes on Hypervisor
func ListHyprVols(pool string) (map[string]string) {
	vols := make(map[string]string) 
	args := []string{"vol-list",pool}
	out := virshcmd(args)
	lines := strings.Split(string(out), "\n")
	for _, ln := range lines {
		words := strings.Fields(ln)
		if len(words)<2  { continue }
		if words[0] == "Name" { continue }
		if len(words[0])<2  { continue }
		vols[words[0]] = words[1]
	}
	return vols
}

func VolPath(pool, vol string) (string, error){
	args := []string{"vol-path","--pool",pool,vol}
	cmd := exec.Command("virsh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}


func UnMapAllDomains() {
	vms := ListVMs()
	for _,vm := range vms {
		UnMapVMBlkDevs(vm)
	}
}


func DisplayVols(vols map[string]string) {
	for name, _ := range vols {
		//fmt.Printf(" >  %s %s  \n", name,path)
		fmt.Printf(" >  %s\n", name)
	}
}

func NextOpenVdx(vm string) (string , error) {
	mappedblks := ListVMblks(vm)
	next  := "vd"
	for _, c := range "abcdefghijklmnopqrstuvwxyz" {
		found := false
		for _,blkmap := range mappedblks {
			if  "vd"+string(c) == blkmap.vdblk {
				found = true
				break
			}
		}
		if  ! found {
			next += string(c)
			break
		}
	}
	if next == "vd"  {
		return  "vdxerror" , errors.New("Can't find an open vdx block handle on VM")
	}
	return next , nil
}

//Attache device from VM
func AttachDisk(vm string, blkdev string) (error) {
	vdxblk,err := NextOpenVdx(vm)
	if  err != nil {return err }
	xml := "<disk type='block' device='disk'>\n"
	xml += "   <driver name='qemu' type='raw' cache='none'/>\n"
	xml += fmt.Sprintf("  <source dev='%s'/>\n",blkdev)
	xml += fmt.Sprintf("  <target dev='%s' bus='virtio'/>\n",vdxblk)
	xml += "</disk>\n"

	file, err := ioutil.TempFile("/tmp", "disk.attach")    
	if err != nil { return err }
	defer os.Remove(file.Name())
	_, err = file.WriteString(xml)
	if err != nil { return err }
	fmt.Println(file.Name()) // For example "dir/prefix054003078"
	args := []string{"attach-device",vm , file.Name(), "--current"}
	cmd := exec.Command("virsh", args...)
	return  cmd.Run()
}



//Detach disk from VM
func DetachDisk(vm string, blkdev string) (error) {
	args := []string{"detach-disk",vm , blkdev}
	cmd := exec.Command("virsh", args...)
	err := cmd.Run()
	if err != nil {
		fmt.Printf("ERROR %+v\n %+v\n",cmd,err)
	}
	return err
}


func ListVGs() (vgs []string ){
	//FIXME
	vgs = []string{"sbvg_datalake"}
	return vgs
}

func StartAllPools() {
	vgs := ListVGs()
	for _,vg := range vgs {
		DefinePool(vg)
		StartPool(vg)
		vols := ListHyprVols(vg)
		DisplayVols(vols)
	}
}

func DefinePool(vg string ) error {
	//pools := ListAllPools()
	//for _,pool := range pools {
	//	if vg == pool {
	//		return
	//	}
	//}
	args := []string{"pool-define-as", vg ,"logical", "--source-name",vg, "--target", "/dev/"+vg}
	cmd := exec.Command("virsh", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func StartPool(vg string ) error  {
	pools := ListPools()
	for _,pool := range pools {
		if vg == pool {
			return nil
		}
	}
	args := []string{"pool-start", vg }
	cmd := exec.Command("virsh", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func UndefinePool(vg string )  {
	args := []string{"pool-destroy", vg }
	virshcmd(args)
	args = []string{"pool-undefine", vg }
	virshcmd(args)
}


func UnMapVMBlkDevs(dom string) {
	if ! IsDomValid(dom ) {
		fmt.Printf("VM not found %s\n", dom)
	}
	mappedblks := ListVMblks(dom)
	for _, mappedblk := range mappedblks {
		DetachDisk(dom,mappedblk.vdblk)
	}
}

func BlkID(blkdev string) (string, error ){
	cmd := exec.Command("blkid", "-po","udev", blkdev)
        out, err := cmd.CombinedOutput()
	if err != nil {
		return  nil , errors.New("Can't find block device on host")
	}
  
	lines := strings.Split(out.String(), "\n")
	for _, ln := range lines {
		chunks := strings.Split(ln, "=")
		if len(chunks) == 2 {
			if chunks[0] == "ID_FS_UUID" {
				return chunks[1] , nil
			}
		}
	}
	return  nil , errors.New("Can't find blockid device on host")
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







