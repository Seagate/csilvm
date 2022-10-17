# CSI plugin for Shared LVM2 Volume Groups 

This is a [container storage interface (CSI)](https://github.com/container-storage-interface/spec) plugin for Linux LVM2 using [lvmlockd](https://man7.org/linux/man-pages/man8/lvmlockd.8.html) for multihost shared Volume Groups.
LVM2 Logical Volumes are dynamically provisions as Kubernetes Physical Volume Claims out of a Volume Group identified by a CSI StorageClass.
A LVM2 Volume Group may have more than one Storage Class for orchestrated control of quality of service and resiliency from the same pool.  

Optimal performance is achieved when node servers are directly connected to an external storage enclosure.
Pods started on nodes without a direct connection to the drives will use iSCSI or NVMeoF for communication with a Controller Agent node.


## Prerequisites
* While not required, the [Seagate Propeller](https://github.com/Seagate/propeller) project provides a robust and stable version of lvmlockd which is supported by LVM2 version 2.3.13 or later when compiled with the enable-lvmlockd-idm option.
Red Hat 8.6 (and derivatives) come with LVM2 v2.3.14.
* The In-Drive Mutex feature of Seagate Nytro 3050 series SAS SSDs can be enabled with IDM feature.  Please contact your Seagate Representative for details.
* Kubernetes version 1.18 or later is recommended.
* The StoLake gRPC agent must be running on all nodes using this CSI Plugin.



# Getting Started

## Enable IDM Locking Mode in LVM2
Validate that IDM option is configured on all controller nodes (those directly connected to the drives).

```bash
# lvm version |grep idm
  Configuration:   ./configure --build=x86_64-redhat-linux-gnu  
  ... 
  --enable-lvmlockd-idm 
  ...
```

If not enabled, [manually recompile](https://www.linuxfromscratch.org/blfs/view/svn/postlfs/lvm2.html) the LVM2 package adding the "--enable-lvmlockd-idm" option when running configure.  Or use a precompiled LVM2 rpm package provided for convenience in the deploy/lvm2 directory.

## Install StoLake gRPC Agent
Install the StoLake gRPC agent on each node of the cluster following the instructions in the project. See https://github.com/Seagate/stolake

## Create a LVM2 Shared Volume Group
When creating a Volume Group that will shared between multiple servers directly connected to the drives (this includes iSCSI and NVMeoF), the --shared flag must be used.  Specify the locktype as idm when using the Seagate lvmlockd service of the Propeller project.  The example below creates a VG with the name of datalake using two drives (sdc, sdd).

```bash
 vgcreate --shared --locktype idm sbvg_datalake /dev/sdc /dev/sdd
```

This command can be run on any of the servers connected to the drives.  Once created the other servers may need to rescan the block devices to discover the new lvm2 VG using the partprobe command

## Deploy CSI Plug-in
Each LVM2 Volume Group requires its own CSI plug-in.  This enables multiple tenant, each with their own K8s namespace to use the same drives as packing storage for the VGs.

The deployment scripts under /deploy matching your Kubernetes version start the CSI Plug-in.  The default is to use a LVM2 VG named "sbvg_datalake".  To start a plug-in for a different VG grep for datalake and change all occurances.

```bash
[root@Simon kubernetes-1.18]# grep -r datalake *
clvm/csi-clvm-provisioner.yaml:            path: /var/lib/kubelet/plugins/datalake.speedboat.seagate.com
clvm/csi-clvm-attacher.yaml:            path: /var/lib/kubelet/plugins/datalake.speedboat.seagate.com
clvm/csi-clvm-plugin.yaml:            - --kubelet-registration-path=/var/lib/kubelet/plugins/datalake.speedboat.seagate.com/csi.sock
clvm/csi-clvm-plugin.yaml:            path: /var/lib/kubelet/plugins/datalake.speedboat.seagate.com
csiclvm.service:ExecStart=/var/speedboat/mercury/csiclvm  -volume-group sbvg_datalake -unix-addr=/var/lib/kubelet/plugins/datalake.speedboat.seagate.com/csi.sock -ovirt-ip=10.2.28.147
```
NOTE: The provisioner name does not include the "sbvg_" prefix.

Then run the deploy.sh script to start the CSI driver controllers, node agents, and sidecars.

Use the drop.sh stop the CSI sidecars, controllers and node agents.

## Create CSI Storage Classes
Every CSI persistent volume is associated to a StorageClass that specifies the provisioner and any attributes unique to that StorageClass. 

Multiple StorageClasses may be defined to use the same provisioner(LVM2 Volume Group's CSI Plug-in) and underlying storage for a mix of RAID of LVs in the VG.

See the storageclass.yaml file for a description of the possible options of a CSICLVM StorageClass which includes, RAID, Datapath and Quality of Service constraints.

Once configured apply the StorageClass to the cluster:

```bash
kubectl apply -f storageclass.yaml
```

## Creating Logical Volumes
The CSI driver will create logic volumes based on the attributes of the CSI Storage Class used.  The driver will generate a cluster unique LVM2 volume name for the CSI Persistent Volume.  Create a yaml file for the Persistent Volume associated to StorageClass.  If you have an existing LV or manually create a LVM2 LV the use the optional volumeHandle to associate the LVM2 LV with the CSI Persistent Volume.   

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: nginx-data-example
spec:
  capacity:
    storage: 100Gi
  accessModes:
    - ReadWriteOnce
  storageClassName: clvmstorageclass
  csi:
    driver: datalake.speedboat.seagate.com
    volumeHandle: NAME.OF.LVM2.LOGICAL.VOLUME.IN.VOLUME.GROUP
```

## Using CSI Persistent Volumes
Use CSICLVM PVs like any other CSI Persistent Volumes by modifying the Pod sped to mount the PV.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginx
spec:
  containers:
  - image: nginx:latest
    imagePullPolicy: Always
    name: nginx
    ports:
    - containerPort: 80
      protocol: TCP
    volumeMounts:
      - mountPath: /var/www
        name: http-root
  volumes:
  - name: http-root
    persistentVolumeClaim:
      claimName: nginx-data-example
```


# Developers

### Setting up your local environment

You need a properly configured Go installation.
See the `Dockerfile` for the version of Go used in CI.
Newer versions of Go should work.

You can `go get` the source code from GitHub.



If you want to work on the code I suggest the following workflow.

1. Fork the repository on GitHub.
1. `mkdir -p $GOPATH/src/github.com/mesosphere`
1. `cd $GOPATH/src/github.com/mesosphere`
1. `git clone <your-fork> csilvm`
1. `cd csilvm`
1. `git remote add upstream https://github.com/mesosphere/csilvm.git`
1. `git checkout master`
1. `git branch --set-upstream-to=upstream/master`

You now have the source code cloned locally and the git `origin` set to your fork.

To develop your new feature you can create a new branch and push it to your fork.

1. `git checkout master`
1. `git pull upstream master` (to make sure you're up-to-date)
1. `git checkout -b my-feature-branch`
1. Make changes to `./pkg/lvm`.
1. `git commit -a -m 'lvm: refactored CreateLogicalVolume tests'`
1. `git push origin my-feature-branch`
1. Create a GitHub PR from your branch against `mesosphere/csilvm:master`.


### Building the binary

You need a properly configured Go installation.

The simplest option is to `go get` the project source from GitHub.

```bash
go get -v github.com/mesosphere/csilvm/cmd/csilvm
```


### Running the tests


In order to run the tests you need
* a modern Linux distribution. (Fedora 27)
* sudo rights. This is necessary to create loop devices and (un)mount volumes.
* docker installed. (docker-ce-18.03.1.ce-1.fc27.x86_64.rpm package)
* the `raid1` and `dm_raid` kernel modules must be loaded. If it isn't, run `modprobe raid1 dm_raid` as root.

Then run

```bash
make sudo-test
```

While developing in only one package it is simpler and faster to run only certain tests.

For example, if you're adding new functionality to the `./pkg/lvm` package you can do
:
```bash
cd ./pkg/lvm
go test -c -i . && sudo ./lvm.test -test.v -test.run=TestMyNewFeature
```


## How does this plugin map to the CSI specification?

This plugin is a CSI-compliant wrapper around the normal `lvm2` command-line utilities.
These include `pvcreate`, `vgcreate`, `lvcreate`, etc.

This plugin implements the "headless, unified plugin" design given in the CSI specification.

In particular, the CSI specification lists the architecture as follows:

```
                            CO "Node" Host(s)
+-------------------------------------------+
|                                           |
|  +------------+           +------------+  |
|  |     CO     |   gRPC    | Controller |  |
|  |            +----------->    Node    |  |
|  +------------+           |   Plugin   |  |
|                           +------------+  |
|                                           |
+-------------------------------------------+

Figure 3: Headless Plugin deployment, only the CO Node hosts run
Plugins. A unified Plugin component supplies both the Controller
Service and Node Service.
```

Every instance of this plugin controls a single LVM2 volume group (VG).
Every CSI volume corresponds to a LVM2 logical volume (LV).
The CSI RPCs map to command-line utility invocations on LVM2 physical volumes (PV), a LVM2 volume group (VG) or LVM2 logical volumes (LV).
For the the exact command-line invocations read the source code, starting at https://github.com/mesosphere/csilvm/blob/master/pkg/csilvm/server.go.


## Project structure

This project is split into `./pkg` and `./cmd` directories.

The `./pkg` directory contains logic that may be used from unit tests.

The `./cmd` directory contains commands that interface between the environment (e.g., parsing command-line options, reading environment variables, etc.) and the logic contained in the `./pkg` directory.

The `./pkg` directory is split into the `./pkg/lvm` and `./pkg/csilvm` packages.

The `./pkg/lvm` package provides a Go wrapper around LVM2 command-line utilities and actions.

The `./pkg/csilvm` package includes all the CSI-related logic and translates CSI RPCs to `./pkg/lvm` function calls.

The `./pkg/csilvm/server.go` file should serve as your entrypoint when reading the code as it includes all the CSI RPC endpoints.


## Deployment

The plugin builds to a single executable binary called `csilvm`.
This binary should be copied to the node.
It is expected that the Plugin Supervisor will launch the binary using the appropriate command-line flags.


### Usage

```
$ ./csilvm --help
Usage of ./csilvm:
  -build-version string
        v0.37-stolake
  -controller
        If set, the agent will server operat as both a node and controller agent.
  -default-fs string
        The default filesystem to format new volumes with (default "xfs")
  -default-volume-size uint
        The default volume size in bytes (default 10737418240)
  -devices string
        A comma-seperated list of devices in the volume group
  -lockfile string
        The path to the lock file used to prevent concurrent lvm invocation by multiple csilvm instances (default "/run/csilvm.lock")
  -node-id string
        The node ID reported via the CSI Node gRPC service (default "Simon")
  -probe-module value
        Probe checks that the kernel module is loaded
  -remove-volume-group
        If set, the volume group will be removed when ProbeNode is called.
  -request-limit int
        Limits backlog of pending requests. (default 10)
  -statsd-format string
        The statsd format to use (one of: classic, datadog) (default "datadog")
  -statsd-max-udp-size int
        The size to buffer before transmitting a statsd UDP packet (default 1432)
  -statsd-udp-host-env-var string
        The name of the environment variable containing the host where a statsd service is listening for stats over UDP
  -statsd-udp-port-env-var string
        The name of the environment variable containing the port where a statsd service is listening for stats over UDP
  -stolake-socket string
        The URL for the StoLake gRPC agent to be used instead of issuing local LVM commands.
  -tag value
        Value to tag the volume group with (can be given multiple times)
  -unix-addr string
        The path to the listening unix socket file
  -unix-addr-env string
        An optional environment variable from which to read the unix-addr
  -volume-group string
        The name of the volume group to manage
```


### Listening socket

The plugin listens on a unix socket.
The unix socket path can be specified using the `-unix-addr=<path>` command-line option.
The unix socket path can also be specified using the `-unix-addr-env=<env-var-name>` option in which case the path will be read from the environment variable of the given name.
It is expected that the CO will connect to the plugin through the unix socket and will subsequently communicate with it in accordance with the CSI specification.


### Logging

The plugin emits fairly verbose logs to `STDERR`.
This is not currently configurable.


### Metrics

The plugin emits metrics in StatsD format. By default, it uses the
[DogStatsD](http://docs.datadoghq.com/guides/dogstatsd/) format which augments
the standard StatsD format with tags.

The format of the StatsD metrics can be set using the `-statsd-format` flag. It
defaults to `datadog` but can be set to `classic` in order to emit metrics in
standard StatsD format.

Metrics are emitted over UDP. The StatsD server's host and port are read from
environment variables. The names of the environment variables that specify the
StatsD server's host and port can be set using `-statsd-udp-host-env-var` and
`-statsd-udp-port-env-var` flags, respectively.

Metrics are emitted with the prefix `csilvm`.

The following metrics are reported:

- csilvm_uptime: the uptime (in seconds) of the process
- csilvm_requests: number of requests served
	tags:
	  `result_type`: one of `success`, `error`
	  `method`: the RPC name, e.g., `/csi.v0.Controller/CreateVolume`
- csilvm_requests_latency_(stddev,mean,lower,count,sum,upper): the request duration (in milliseconds)
	tags:
	  `method`: the RPC name, e.g., `/csi.v0.Controller/CreateVolume`
- csilvm_volumes: the number of active logical volumes
- csilvm_bytes_total: the total number of bytes in the volume group
- csilvm_bytes_free: the number of bytes available for creating a linear logical volume
- csilvm_bytes_used: the number of bytes allocated to active logical volumes
- csilvm_pvs: the number of physical volumes in the volume group
- csilvm_missing_pvs: the number of pvs given on the command-line but are not found in the volume group
- csilvm_unexpected_pvs: the number of pvs not given on the command-line but are found in the volume group
- csilvm_lookup_pv_errs: the number of errors encountered while looking for pvs specified on the command-line

Furthermore, all metrics are tagged with `volume-group` set to the
`-volume-group` command-line option.

### Runtime dependencies

The following command-line utilties must be present in the `PATH`:

* the various lvm2 cli utilities (`pvscan`, `vgcreate`, etc.)
* `udevadm`
* `blkid`
* `mkfs`
* `file`
* the filesystem listed as `-default-fs` (defaults to: `xfs`)

For RAID1 support the `raid1` and `dm_raid` kernel modules must be available.

This plugin's tests are run in a centos 7.3.1611 container with lvm2-2.02.183 installed from source.
It should work with newer versions of lvm2 that are backwards-compatible in their command-line interface.
It may work with older versions.



## Notes

### Logical volume naming

The volume group name is specified at startup through the `-volume-group` argument.

Logical volume names are derived from randomly generated, base36-encoded numbers and are prefixed with `csilv`, for example: `csilv9T8s7d3`.

The CO-specified volume name is captured in a LV tag conforming to one of the following formats:

* `VN.<CO-specified-name>`, if the CO-specified name contains *only* characters safe for LVM tags (`A-Z a-z 0-9 + _ . -`).
* `VN+<base64-rawurlencode(CO-specified-name)>`, otherwise. Encoding is performed without padding.

Examples:

* If the CO-specified volume name is `test-volume`, then the generated LV tag is `VN.test-volume`.
* If the CO-specified volume name is `hello volume`, then the generated LV tag is `VN+aGVsbG8gdm9sdW1l`.


### SINGLE_NODE_READER_ONLY

It is not possible to bind mount a device as 'ro' and thereby prevent write access to it.

As such, this plugin does not support the `SINGLE_NODE_READER_ONLY` access mode for a
volume of access type `BLOCK_DEVICE`.

# Issues

You may create a new [bug](https://jira.mesosphere.com/secure/CreateIssueDetails!init.jspa?pid=14105&issuetype=1&components=20732&customfield_12300=3&summary=CSILVM%3a+bug+summary+goes+here&description=Environment%3a%0d%0dWhat+you+attempted+to+do%3a%0d%0dThe+result+you+expected%3a%0d%0dThe+result+you+saw+instead%3a%0d&priority=3) or a new [task](https://jira.mesosphere.com/secure/CreateIssueDetails!init.jspa?pid=14105&issuetype=3&components=20732&customfield_12300=3&summary=CSILVM%3a+task+summary+goes+here&priority=3).


# Authors

* @TProhofsky
* @gpaul
* @jdef
* @jieyu


# License

This project is licensed under the Apache 2.0 License - see the LICENSE file for details.
