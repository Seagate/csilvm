#!/usr/bin/env bash

#kubectl delete -f clvm/csi-clvm-attacher.yaml
#kubectl delete -f clvm/csi-clvm-plugin.yaml      
#kubectl delete -f clvm/csi-clvm-provisioner.yaml  
#kubectl delete -f clvm/csi-clvm-snapshotter.yaml
#kubectl delete -f clvm/csi-clvm-driverinfo.yaml  
#kubectl delete -f clvm/csi-clvm-resizer.yaml      

#kubectl delete daemonset csi-clvmplugin
# kubectl get pods --no-headers=true | awk '/csi-clvm/{print $1}'| xargs  kubectl delete pod

# exit


# This script captures the steps required to deploy the clvm  plugin driver.  

set -e
set -o pipefail

BASE_DIR=$(dirname "$0")


# deleting  clvm plugin and registrar sidecar
echo "dropping clvm components"
for i in $(ls ${BASE_DIR}/clvm/*.yaml | sort); do
    echo "   $i"
    modified="$(cat "$i" | while IFS= read -r line; do
        nocomments="$(echo "$line" | sed -e 's/ *#.*$//')"
        if echo "$nocomments" | grep -q '^[[:space:]]*image:[[:space:]]*'; then
            # Split 'image: quay.io/k8scsi/csi-attacher:v1.0.1'
            # into image (quay.io/k8scsi/csi-attacher:v1.0.1),
            # registry (quay.io/k8scsi),
            # name (csi-attacher),
            # tag (v1.0.1).
            image=$(echo "$nocomments" | sed -e 's;.*image:[[:space:]]*;;')
            registry=$(echo "$image" | sed -e 's;\(.*\)/.*;\1;')
            name=$(echo "$image" | sed -e 's;.*/\([^:]*\).*;\1;')
            tag=$(echo "$image" | sed -e 's;.*:;;')

            # Variables are with underscores and upper case.
            varname=$(echo $name | tr - _ | tr a-z A-Z)

            # Now replace registry and/or tag, if set as env variables.
            # If not set, the replacement is the same as the original value.
            # Only do this for the images which are meant to be configurable.
            if update_image "$name"; then
                prefix=$(eval echo \${${varname}_REGISTRY:-${IMAGE_REGISTRY:-${registry}}}/ | sed -e 's;none/;;')
                if [ "$IMAGE_TAG" = "canary" ] &&
                   [ -f ${BASE_DIR}/canary-blacklist.txt ] &&
                   grep -q "^$name\$" ${BASE_DIR}/canary-blacklist.txt; then
                    # Ignore IMAGE_TAG=canary for this particular image because its
                    # canary image is blacklisted in the deployment blacklist.
                    suffix=$(eval echo :\${${varname}_TAG:-${tag}})
                else
                    suffix=$(eval echo :\${${varname}_TAG:-${IMAGE_TAG:-${tag}}})
                fi
                line="$(echo "$nocomments" | sed -e "s;$image;${prefix}${name}${suffix};")"
            fi
            echo "        using $line" >&2
        fi
        echo "$line"
    done)"
    if ! echo "$modified" | kubectl delete -f -; then
        echo "Deleting  $i:"
        echo "$modified"
        exit 1
    fi
done


kubectl apply -f "${BASE_DIR}/snapshotter/csi-clvm-snapshotclass.yaml" 

