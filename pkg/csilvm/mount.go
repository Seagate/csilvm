package csilvm

import (
	"bufio"
	"bytes"
	"errors"
	"github.com/Seagate/csiclvm/pkg/virsh"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strings"
	"os/exec"
)

/*
3.5	/proc/<pid>/mountinfo - Information about mounts
--------------------------------------------------------

This file contains lines of the form:

36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
(1)(2)(3)   (4)   (5)      (6)      (7)   (8) (9)   (10)         (11)

(1) mount ID:  unique identifier of the mount (may be reused after umount)
(2) parent ID:  ID of parent (or of self for the top of the mount tree)
(3) major:minor:  value of st_dev for files on filesystem
(4) root:  root of the mount within the filesystem
(5) mount point:  mount point relative to the process's root
(6) mount options:  per mount options
(7) optional fields:  zero or more fields of the form "tag[:value]"
(8) separator:  marks the end of the optional fields
(9) filesystem type:  name of filesystem of the form "type[.subtype]"
(10) mount source:  filesystem specific information or "none"
(11) super options:  per super block options

~ https://www.kernel.org/doc/Documentation/filesystems/proc.txt
*/

type mountpoint struct {
	root        string
	path        string
	fstype      string
	mountopts   []string
	mountsource string
	blockpath   string  //Full disk-by-path of source
	datapath    string  //Connection Method: SAS, ISCSI,...
}

func (m *mountpoint) isReadonly() bool {
	for _, opt := range m.mountopts {
		if opt == "ro" {
			return true
		}
	}
	return false
}

func listMounts() (mounts []mountpoint, err error) {
	if virsh.ProxyMode() {
		buf, err := virsh.MountInfo()
		if err != nil {
			return nil, err
		}
		return parseMountinfo(buf)
	}
	buf, err := ioutil.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountinfo(buf)
}

func parseMountinfo(buf []byte) (mounts []mountpoint, err error) {
	for _, line := range strings.Split(string(buf), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// There may be one or more optional fields between column 6
		// and before the '-'.
		foundSep := false
		sepoffset := 6
		for ; sepoffset < len(fields); sepoffset++ {
			if fields[sepoffset] == "-" {
				foundSep = true
				break
			}
		}
		if !foundSep {
			return nil, errors.New("Failed to parse /proc/self/mountinfo")
		}
		blockpath := getBlockPath(fields[sepoffset+2])
		if  blockpath == "" {
			fmt.Printf("NO BLOCK PATH PARSING:: %s :: SEP %d \n",line,sepoffset)
		} else {
			mount := mountpoint{
				root:        fields[3],
				path:        fields[4],
				fstype:      fields[sepoffset+1],
				mountopts:   strings.Split(fields[5], ","),
				mountsource: fields[sepoffset+2],
				blockpath:   blockpath,
				datapath:    dataPathType(blockpath),
			}
			if  mount.datapath == "" {
				fmt.Printf("NO DATAPATH PARSING:: %s ::%v \n",line, mount)
			} else {
				mounts = append(mounts, mount)
			}
		}
	}
	return mounts, nil
}

// getMountAt returns the first `mountpoint` that is mounted at the
// given path.
func getMountAt(path string) (*mountpoint, error) {
	mounts, err := getMountsAt(path)
	if err != nil {
		return nil, err
	}
	for _, mp := range mounts {
		return &mp, nil
	}
	return nil, nil
}

// getMountsAt returns all `mountpoint` that are mounted at the given
// path.
func getMountsAt(path string) ([]mountpoint, error) {
	mounts, err := listMounts()
	if err != nil {
		return nil, err
	}
	var mps []mountpoint
	for _, mp := range mounts {
		if mp.path == path {
			mps = append(mps, mp)
		}
	}
	return mps, nil
}

func getBlockPath(blkdev string) string {
	// if LVM2 LV then return blkdev and blockpath
	if len(blkdev) > 10 {
		if blkdev[0:10] == "/dev/sbvg_" {
			return blkdev 
		}
	}
	cmd := exec.Command("ls", "-lt", "/dev/disk/by-path/")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		fmt.Printf("BY-PATH FAILED::%v \n", err)
		return "" 
	}
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		chunks := strings.Split(scanner.Text(), "->")
		if len(chunks) < 2 {
			continue
		}
		if filepath.Base(chunks[1]) != filepath.Base(blkdev) {
			continue
		}
		words := strings.Fields(chunks[0])
		if  len(words) > 1 {
			return words[len(words)-1]
		}
	}
	fmt.Printf("DBG FAILED to find %s in BY-PATH list  \n", blkdev )
	//fmt.Printf("DBG FAILED to find %s in BY-PATH list ::%s \n", blkdev, out.String())
	return ""
}

func dataPathType(path string) string {
	// If blockpath is LVM2 LV then return direct
	if len(path) > 10 {
		if path[0:10] == "/dev/sbvg_" {
			return "direct" 
		}
	}
	chunks := strings.Split(path,"-")
	if len(chunks) < 4 {
		fmt.Printf("FAILED to parse BY-PATH ::%+v ", chunks)
		return ""
	}
	return chunks[2]
}


