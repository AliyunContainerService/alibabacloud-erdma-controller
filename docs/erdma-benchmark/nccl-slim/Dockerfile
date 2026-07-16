# 精简 eRDMA + NCCL 测试镜像 (nccl-slim)
#
# 目的：把 :nccl(~7.6GB 压缩)瘦身成 ~250MB，让跨区/冷集群拉取从"过夜"变成"分把钟"。
# 思路：多阶段——从 :nccl 里把运行期真正需要的文件挑出来，装进纯 ubuntu:22.04。
#
#   砍掉：PyTorch 全家桶(5.4G)、CUDA devel 工具链/nvcc(2G)、cuDNN/cuBLAS/triton 等——都与 nccl-tests 无关
#   保留：libnccl、libcudart、openmpi、nccl-tests 二进制、perftest、ibv_* 诊断工具
#   关键：eRDMA provider 必须从 :nccl 拷贝(ubuntu22.04 apt 的 rdma-core 不含 erdma provider)
#
# GDR 能力与 :nccl 完全等价(libnccl+erdma provider 齐全)；能否真正用 GDR 取决于机型
# (裸金属可，VM 因 ACS 不可)，与镜像无关。CUDA driver(libcuda)由 nvidia-container-runtime 从宿主挂入。
#
# 构建(必须 x86_64；本地已有 :nccl 镜像即可，无需联网重编 nccl-tests)：
#   docker build -t registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl-slim .
#   docker push  registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl-slim
# 若在 arm 机器(如 Apple Silicon)上：
#   docker buildx build --platform linux/amd64 -t <tag>:nccl-slim --push .
#
# 注意：本镜像"派生自" :nccl。若 :nccl 里的 NCCL 版本更新了，重建本镜像即可同步。

FROM registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl AS src
COPY assemble.sh /assemble.sh
RUN bash /assemble.sh

FROM ubuntu:22.04
# 运行时依赖：ssh(mpirun 跨节点拉起)、openmpi 依赖的系统库、libgomp
RUN sed -i 's|http://archive.ubuntu.com/ubuntu/|http://mirrors.aliyun.com/ubuntu/|g; s|http://security.ubuntu.com/ubuntu/|http://mirrors.aliyun.com/ubuntu/|g' /etc/apt/sources.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
      openssh-server openssh-client ca-certificates iproute2 \
      libgomp1 libnl-3-200 libnl-route-3-200 libevent-core-2.1-7 libevent-pthreads-2.1-7 \
      libhwloc15 libudev1 && \
    apt-get clean && rm -rf /var/lib/apt/lists/* && mkdir -p /run/sshd

COPY --from=src /out/opt/build /opt/nccl-tests/build
COPY --from=src /out/bin/ /usr/bin/
COPY --from=src /out/lib/ /usr/lib/x86_64-linux-gnu/
COPY --from=src /out/etc/libibverbs.d/ /etc/libibverbs.d/
RUN ldconfig && ln -sf /usr/lib/x86_64-linux-gnu/libnccl.so.2 /usr/lib/x86_64-linux-gnu/libnccl.so

ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility \
    OPAL_PREFIX=/usr \
    PATH=/usr/bin:/usr/local/bin:/opt/nccl-tests/build:$PATH
CMD ["sleep", "infinity"]
