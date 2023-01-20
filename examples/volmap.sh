# Copyright: 2021 Seagate Technology LLC and/or its Affiliates

PVCs=`kubectl get pvc -o custom-columns="PVC:metadata.name,PV:spec.volumeName" --no-headers |tr -s " "`
declare -A PVCtoPV
KEY=""
for PVC in $PVCs; do
   if [ "${KEY}" = "" ]; then
      KEY=$PVC
   else
      PVCtoPV[$KEY]=$PVC
      KEY=""
   fi

done

#for x in "${!PVCtoPV[@]}"; do printf "[%s]=%s\n" "$x" "${PVCtoPV[$x]}" ; done


PVs=`kubectl get pv --no-headers -o custom-columns=PVNAME:.metadata.name,VOL:.spec.csi.volumeHandle`
declare -A PVtoVOL
KEY=""
for PV in $PVs; do
   if [ "${KEY}" = "" ]; then
      KEY=$PV
   else
      PVtoVOL[$KEY]=$PV
      KEY=""
   fi
done

#for x in "${!PVtoVOL[@]}"; do printf "[%s]=%s\n" "$x" "${PVtoVOL[$x]}" ; done

printf "%-30s" "POD:CONTAINER "
printf "%-20s" "PVC NAME"
printf "%-43s" "PV NAME"
printf "%-16s\n" "LOGICAL VOLUME "

PODS=`kubectl get pod --no-headers -o custom-columns=POD:metadata.name,CONT:.spec.containers[*].name,VOL:.spec.volumes[*].persistentVolumeClaim.claimName|tr -s " "`

PNAME=""
CNAME=""
for POD in $PODS; do
   if [ "${PNAME}" = "" ]; then
      PNAME=$POD
   elif [ "${CNAME}" = "" ]; then
      CNAME=$POD
   else
      if [[ "${POD}" != "<none>" ]]; then
         printf "%-30s" "${PNAME}:$CNAME "
         printf "%-20s" $POD
         printf "%-43s" "${PVCtoPV[$POD]}   "
         echo  "${PVtoVOL[${PVCtoPV[$POD]}]}"
      fi
      PNAME=""
      CNAME=""
   fi
done


