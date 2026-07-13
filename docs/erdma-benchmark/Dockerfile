# eRDMA + NCCL 基准测试镜像
# 已构建并推送到: registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
#
# 基础镜像: ACS pytorch (Ubuntu 22.04 + CUDA 12.2 runtime + NCCL + torch 2.6)
# 叠加内容: eRDMA userspace verbs(含 erdma provider) + perftest + openmpi + nccl-tests
#
# 构建(需 x86_64 机器, 建议在 cn-hangzhou 的 ECS 上走 VPC 内网):
#   docker build -t registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl .
#   docker push  registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
FROM registry-cn-hangzhou-vpc.ack.aliyuncs.com/acs/pytorch-examples:2.6.0-cuda12.2-1

ENV DEBIAN_FRONTEND=noninteractive

# --- eRDMA / RDMA userspace verbs stack from the official Aliyun eRDMA apt repo ---
# Ubuntu 22.04 的自带 ibverbs-providers(39.0) 早于 erdma provider, 用阿里云打包的
# rdma-core(jammy) 才带 erdma provider.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gnupg wget \
    && mkdir -p /etc/apt/keyrings \
    && wget -qO /etc/apt/keyrings/erdma.asc https://mirrors.aliyun.com/erdma/GPGKEY \
    && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/erdma.asc] https://mirrors.aliyun.com/erdma/apt/ubuntu jammy/erdma main" \
        > /etc/apt/sources.list.d/erdma.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        libibverbs1 ibverbs-providers ibverbs-utils librdmacm1 rdmacm-utils perftest \
    && rm -rf /var/lib/apt/lists/*

# --- build toolchain: nvcc(匹配 CUDA 12.2) + host compiler + MPI + ssh ---
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential git cuda-nvcc-12-2 cuda-cudart-dev-12-2 \
        openmpi-bin libopenmpi-dev openssh-server openssh-client \
    && rm -rf /var/lib/apt/lists/*

# --- NCCL: 自带的 2.21.5 早于 Blackwell(sm_120), 在 RTX PRO / B 系列上 kernel 会报
# "cuda invalid argument". 从 CUDA repo 装 Blackwell-capable 的 NCCL. ---
RUN apt-get update && apt-get install -y --no-install-recommends \
        libnccl2=2.27.7-1+cuda12.9 libnccl-dev=2.27.7-1+cuda12.9 \
    && rm -rf /var/lib/apt/lists/*

# --- 编译 nccl-tests(源码放在构建上下文 ./nccl-tests, 避免 ECS 上 clone github 超时) ---
# 用 compute_90 PTX, 靠驱动 JIT 到 sm_120(Blackwell); sm_90 SASS 保留 Hopper 原生支持.
COPY nccl-tests /opt/nccl-tests
RUN cd /opt/nccl-tests \
    && make -j"$(nproc)" \
        MPI=1 MPI_HOME=/usr/lib/x86_64-linux-gnu/openmpi CUDA_HOME=/usr/local/cuda \
        NVCC_GENCODE="-gencode=arch=compute_90,code=sm_90 -gencode=arch=compute_90,code=compute_90"

# 运行期: 优先系统 NCCL 2.27.7(而非自带 2.21.5) + openmpi/cuda
ENV LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/local/cuda/lib64:/usr/lib/x86_64-linux-gnu/openmpi/lib:${LD_LIBRARY_PATH}
ENV PATH=/opt/nccl-tests/build:/usr/local/cuda/bin:${PATH}
ENV OMPI_ALLOW_RUN_AS_ROOT=1 OMPI_ALLOW_RUN_AS_ROOT_CONFIRM=1
WORKDIR /workspace
