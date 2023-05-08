

docker build -f Dockerfile.csidriver -t ghcr.io/tprohofsky/csiclvm:dev .

export CR_PAT=`cat /mnt/html/speedboat/token.txt`

echo $CR_PAT |docker login ghcr.io -u tprohofsky --password-stdin

docker push  ghcr.io/tprohofsky/csiclvm:dev



