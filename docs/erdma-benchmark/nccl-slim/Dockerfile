# 精简 eRDMA + NCCL 测试镜像 (nccl-slim)
#
# 目的：把 :nccl(~7.6GB 压缩)瘦身成 ~400MB，让跨区/冷集群拉取从"过夜"变成"分把钟"。
# 思路：多阶段——从 :nccl 里把运行期真正需要的文件挑出来，装进纯 ubuntu:22.04。
#
#   砍掉：PyTorch 全家桶(5.4G)、CUDA devel 工具链/nvcc(2G)、cuDNN/cuBLAS/triton 等——都与 nccl-tests 无关
#   保留：libcudart、openmpi、nccl-tests 二进制、perftest、ibv_* 诊断工具
#   关键：eRDMA provider 必须从 :nccl 拷贝(ubuntu22.04 apt 的 rdma-core 不含 erdma provider)
#
# libnccl 不从 :nccl 拷，而是本镜像直接从 NVIDIA CUDA repo 装 2.27.7：:nccl 里两份 libnccl
# 都早于 Blackwell(/usr/lib 下 2.19.3 被 dpkg hold；site-packages 里 PyTorch 自带 2.21.5)，
# 在 sm_120 上一律 "enqueue.cc:60 Cuda failure 'invalid argument'"，nccl-tests 完全跑不起来。
# nccl-tests 二进制是 compute_90 PTX 编译的，配 2.27.7 由驱动 JIT 到 sm_120，实测正常。
#
# GDR 能力与 :nccl 等价(libnccl+erdma provider 齐全)；能否真正用 GDR 取决于机型
# (裸金属可，VM 因 ACS 不可)，与镜像无关。CUDA driver(libcuda)由 nvidia-container-runtime 从宿主挂入。
#
# 构建(必须 x86_64；需能访问 :nccl 与 NVIDIA CUDA repo)：
#   docker build -t registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl-slim .
#   docker push  registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl-slim
# 若在 arm 机器(如 Apple Silicon)上：
#   docker buildx build --platform linux/amd64 -t <tag>:nccl-slim --push .
#
# 注意：本镜像只从 :nccl 取 nccl-tests 二进制 / perftest / erdma provider；NCCL 版本由下面的
# NCCL_VERSION 决定，与 :nccl 无关。要换 NCCL 版本改这里即可，不必重建 :nccl。

FROM registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl AS src
COPY assemble.sh /assemble.sh
RUN bash /assemble.sh

FROM ubuntu:22.04
# Blackwell(sm_120) 需要 NCCL >= 2.27
ARG NCCL_VERSION=2.27.7-1+cuda12.9

# 运行时依赖：ssh(mpirun 跨节点拉起)、openmpi 依赖的系统库、libgomp
RUN sed -i 's|http://archive.ubuntu.com/ubuntu/|http://mirrors.aliyun.com/ubuntu/|g; s|http://security.ubuntu.com/ubuntu/|http://mirrors.aliyun.com/ubuntu/|g' /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
      openssh-server openssh-client ca-certificates iproute2 wget \
      libgomp1 libnl-3-200 libnl-route-3-200 libevent-core-2.1-7 libevent-pthreads-2.1-7 \
      libhwloc15 libudev1 && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && mkdir -p /run/sshd

# libnccl：从 NVIDIA CUDA repo 装 Blackwell-capable 版本(不用 :nccl 里的旧 NCCL)
RUN wget -q https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2204/x86_64/cuda-keyring_1.1-1_all.deb -O /tmp/cuda-keyring.deb && \
    dpkg -i /tmp/cuda-keyring.deb && rm -f /tmp/cuda-keyring.deb && \
    apt-get update && \
    apt-get install -y --no-install-recommends "libnccl2=${NCCL_VERSION}" && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=src /out/opt/build /opt/nccl-tests/build
COPY --from=src /out/bin/ /usr/bin/
COPY --from=src /out/lib/ /usr/lib/x86_64-linux-gnu/
COPY --from=src /out/etc/libibverbs.d/ /etc/libibverbs.d/
RUN ldconfig && ln -sf /usr/lib/x86_64-linux-gnu/libnccl.so.2 /usr/lib/x86_64-linux-gnu/libnccl.so

# 断言最终 libnccl >= 2.27：上面 COPY /out/lib 若哪天又带进一份旧 libnccl 会盖掉 apt 的，
# 必须在这里拦住，否则镜像在 Blackwell 上静默不可用。
RUN set -eu; \
    ver=$(grep -aoE '2\.[0-9]+\.[0-9]+\+cuda[0-9.]+' /usr/lib/x86_64-linux-gnu/libnccl.so.2 | sort -u | head -1); \
    echo "=== libnccl in image: ${ver:-unknown} ==="; \
    case "$ver" in \
      2.2[7-9].*|2.[3-9][0-9].*) ;; \
      *) echo "FATAL: libnccl 是 ${ver:-unknown}, 需 >=2.27(Blackwell sm_120)" >&2; exit 1;; \
    esac; \
    ldd /opt/nccl-tests/build/all_reduce_perf | grep -q 'not found' && { echo "FATAL: all_reduce_perf 有未解析依赖" >&2; exit 1; } || true

ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility \
    OPAL_PREFIX=/usr \
    PATH=/usr/bin:/usr/local/bin:/opt/nccl-tests/build:$PATH
CMD ["sleep", "infinity"]
