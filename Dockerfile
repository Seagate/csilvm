# Update the README when this base image and/or the version of lvm2 (below) is updated.
FROM rockylinux/rockylinux:8.4

RUN yum install -y gcc gcc-c++ make git util-linux xfsprogs file libaio libaio-devel.x86_64

ARG LVM_VERSION=LVM2.2.03.13

ENV LVM2_DOWNLOAD_URL https://www.sourceware.org/pub/lvm2/$LVM_VERSION.tgz

RUN curl -fsSL "$LVM2_DOWNLOAD_URL" -o $LVM_VERSION.tgz && \
      tar -xzvf $LVM_VERSION.tgz && \
      cd $LVM_VERSION && \
      ./configure && \
      make && \
      make install && \
      ldconfig && \
      cd .. && \
      rm -f $LVM_VERSION.tgz

ENV GOLANG_VERSION 1.20.2
ENV GOLANG_DOWNLOAD_URL https://golang.org/dl/go$GOLANG_VERSION.linux-amd64.tar.gz
ENV GOLANG_DOWNLOAD_SHA256 4eaea32f59cde4dc635fbc42161031d13e1c780b87097f4b4234cfce671f1768

RUN rm -rf /usr/local/go && \
      curl -fsSL "$GOLANG_DOWNLOAD_URL" -o golang.tar.gz && \
      echo "$GOLANG_DOWNLOAD_SHA256  golang.tar.gz" | sha256sum -c - && \
      tar -C /usr/local -xzf golang.tar.gz && \
      rm -f golang.tar.gz

ENV GOPATH /go
ENV PATH /go/bin:$PATH
ENV PATH /usr/local/go/bin:$PATH

RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && \ 
    mkdir -p /go/src/github.com/Seagate/csiclvm

# We explicitly disable use of lvmetad as the cache appears to yield inconsistent results,
# at least when running in docker.
RUN sed -i 's/udev_rules = 1/udev_rules = 0/' /etc/lvm/lvm.conf && \
    sed -i 's/udev_sync = 1/udev_sync = 0/' /etc/lvm/lvm.conf && \
    sed -i 's/use_lvmetad = 1/use_lvmetad = 0/' /etc/lvm/lvm.conf

WORKDIR /go/src/github.com/Seagate/csiclvm
