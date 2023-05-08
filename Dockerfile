# Update the README when this base image and/or the version of lvm2 (below) is updated.
FROM rockylinux/rockylinux:8.7

RUN yum install -y gcc gcc-c++ make git util-linux xfsprogs file libaio libaio-devel.x86_64 golang


ENV GOPATH /go
ENV PATH /go/bin:$PATH
ENV PATH /usr/local/go/bin:$PATH

#RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && \ 
RUN  mkdir -p /go/src/github.com/Seagate/csiclvm

# We explicitly disable use of lvmetad as the cache appears to yield inconsistent results,
# at least when running in docker.

WORKDIR /go/src/github.com/Seagate/csiclvm
