#!/bin/sh
set -e

mkdir -p bin/

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)
        ARCH_SUFFIX="amd64"
        ;;
    aarch64|arm64)
        ARCH_SUFFIX="arm64"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# download lightning and sync_diff_inspector
TOOLS_TAG="nightly"
wget http://download.pingcap.org/tidb-toolkit-$TOOLS_TAG-linux-$ARCH_SUFFIX.tar.gz -O tools.tar.gz
tar -xzvf tools.tar.gz
mv tidb-toolkit-$TOOLS_TAG-linux-$ARCH_SUFFIX/bin/* bin/

# download minio
wget https://dl.min.io/server/minio/release/linux-$ARCH_SUFFIX/minio -O bin/minio
chmod a+x bin/minio

wget https://dl.minio.io/client/mc/release/linux-$ARCH_SUFFIX/mc -O bin/mc
chmod a+x bin/mc

go get github.com/ma6174/snappy@15869b0666f67839ecf86cd29ef1452ddcd79cb8
go install github.com/ma6174/snappy@15869b0666f67839ecf86cd29ef1452ddcd79cb8

wget https://github.com/facebook/zstd/releases/download/v1.5.2/zstd-1.5.2.tar.gz
tar xvfz zstd-1.5.2.tar.gz
cd zstd-1.5.2
make
