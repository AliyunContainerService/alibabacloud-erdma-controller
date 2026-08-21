#!/bin/bash
# 在 :nccl(ubuntu22.04)镜像里，把运行期真正需要的文件收敛到 /out，
# 供 Dockerfile final stage 拷进精简 base。核心：ldd 闭包 + dlopen 的插件目录(openmpi mca / ibverbs provider)。
set -eux
mkdir -p /out/lib /out/bin /out/opt /out/etc/libibverbs.d

# 1) nccl-tests 二进制
cp -a /opt/nccl-tests/build /out/opt/build

# 2) 关键可执行文件：mpirun 家族 + perftest + ibverbs 诊断工具
for b in mpirun mpiexec orterun orted \
         ib_write_bw ib_write_lat ib_send_bw ib_read_bw ib_read_lat \
         ibv_devinfo ibv_devices rdma; do
  if p=$(command -v "$b" 2>/dev/null); then
    cp -aL "$p" /out/bin/ || true
  fi
done

# 3) 强制包含的核心库(cuda runtime)
# 注意: 这里刻意"不"从 :nccl 拷 libnccl —— :nccl 里两份 libnccl 都早于 Blackwell
# (/usr/lib 下 2.19.3 被 dpkg hold, site-packages 里 PyTorch 自带 2.21.5),
# 在 sm_120 上一律 "enqueue.cc:60 Cuda failure 'invalid argument'".
# libnccl 由 final stage 直接从 NVIDIA CUDA repo 装 >=2.27, 见 Dockerfile.
cp -aL /usr/local/cuda-12.2/targets/x86_64-linux/lib/libcudart.so.12 /out/lib/

# 4) dlopen 的插件目录整体拷贝(ldd 抓不到)
cp -a /usr/lib/x86_64-linux-gnu/openmpi   /out/lib/openmpi
cp -a /usr/lib/x86_64-linux-gnu/libibverbs /out/lib/libibverbs
cp -a /etc/libibverbs.d/. /out/etc/libibverbs.d/ 2>/dev/null || true

# 5) ibverbs provider 的符号链接目标(../liberdma.so.* 等)
for p in erdma mlx5 mlx4 efa hns mana; do
  cp -a /usr/lib/x86_64-linux-gnu/lib${p}.so.* /out/lib/ 2>/dev/null || true
done

# 6) 计算 ldd 闭包：扫描 二进制 + libnccl + provider + mca 插件
SCAN="$(find /out/opt/build -type f -perm -u+x) $(ls /out/bin/* 2>/dev/null)"
SCAN="$SCAN /out/lib/libcudart.so.12"
SCAN="$SCAN $(find /usr/lib/x86_64-linux-gnu/libibverbs -type f 2>/dev/null)"
SCAN="$SCAN $(find /usr/lib/x86_64-linux-gnu/openmpi -name '*.so*' 2>/dev/null)"
for f in $SCAN; do ldd "$f" 2>/dev/null | awk '/=> \//{print $3}'; done | sort -u > /tmp/closure.txt

# 7) 拷闭包里的库；glibc 核心交给 base(避免破坏 ABI)
# libnccl 也必须排除：nccl-tests 二进制链接的是 :nccl 里的旧 libnccl(经 LD_LIBRARY_PATH
# 解析到 site-packages 的 2.21.5)，跟进闭包会盖掉 final stage 里 apt 装的 2.27.7。
while read -r l; do
  [ -f "$l" ] || continue
  bn=$(basename "$l")
  case "$bn" in
    libc.so.*|ld-linux*|libm.so.*|libpthread.so.*|libdl.so.*|librt.so.*|libresolv.so.*|libnsl.so.*|libutil.so.*) continue;;
    libnccl.so.*) continue;;
  esac
  cp -aL "$l" /out/lib/ 2>/dev/null || true
done < /tmp/closure.txt

echo "=== /out 体积 ==="; du -sh /out; du -sh /out/*
