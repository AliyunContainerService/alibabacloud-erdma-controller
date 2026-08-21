# eRDMA + NCCL 基准测试镜像
# 已构建并推送到: registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
#
# 基础镜像: ACS pytorch (Ubuntu 22.04 + CUDA 12.2 runtime + NCCL + torch 2.6)
# 叠加内容: eRDMA userspace verbs(含 erdma provider) + perftest + openmpi + nccl-tests
#
# 构建(需 x86_64 机器, 建议在 cn-hangzhou 的 ECS 上走 VPC 内网):
#   docker build -t registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl .
#   docker push  registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
#
# BASE_IMAGE 默认用公网地址(公网 = VPC 地址去掉 host 里的 "-vpc"), 这样集群外也能构建;
# 在 VPC 内构建可 --build-arg BASE_IMAGE=registry-cn-hangzhou-vpc.ack.aliyuncs.com/acs/pytorch-examples:2.6.0-cuda12.2-1
ARG BASE_IMAGE=registry-cn-hangzhou.ack.aliyuncs.com/acs/pytorch-examples:2.6.0-cuda12.2-1
FROM ${BASE_IMAGE}

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

# --- NCCL: 自带的早于 Blackwell(sm_120), 在 RTX PRO / B 系列上 kernel 会报
# "cuda invalid argument"(enqueue.cc:60). 从 CUDA repo 装 Blackwell-capable 的 NCCL.
#
# 基础镜像把 libnccl2 apt-mark hold 住了(连同 libcublas-12-2/libcudnn8), 不先 unhold 的话
# apt 直接失败: "E: Held packages were changed and -y was used without
# --allow-change-held-packages" —— 之前推上去的 :nccl 就是因此停留在基础镜像自带的 2.19.3.
# 装完立刻断言版本, 防止再次静默发布一个在 Blackwell 上不可用的镜像. ---
ARG NCCL_VERSION=2.27.7-1+cuda12.9
RUN apt-get update \
    && apt-mark unhold libnccl2 \
    && apt-get install -y --no-install-recommends \
        "libnccl2=${NCCL_VERSION}" "libnccl-dev=${NCCL_VERSION}" \
    && rm -rf /var/lib/apt/lists/* \
    && ver=$(grep -aoE '2\.[0-9]+\.[0-9]+\+cuda[0-9.]+' /usr/lib/x86_64-linux-gnu/libnccl.so.2 | sort -u | head -1) \
    && echo "=== libnccl in /usr/lib: ${ver:-unknown} ===" \
    && case "$ver" in \
         2.2[7-9].*|2.[3-9][0-9].*) ;; \
         *) echo "FATAL: libnccl 是 ${ver:-unknown}, 需 >=2.27(Blackwell sm_120)" >&2; exit 1;; \
       esac

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

# 断言运行期真的加载 >=2.27: site-packages 里 PyTorch 自带的旧 libnccl 仍在, 靠上面的
# LD_LIBRARY_PATH 顺序遮住它 —— 一旦顺序被改动就会静默回退到旧版, 这里直接拦住.
RUN set -eu; \
    lib=$(ldd /opt/nccl-tests/build/all_reduce_perf | awk '/libnccl\.so\.2/{print $3}'); \
    ver=$(grep -aoE '2\.[0-9]+\.[0-9]+\+cuda[0-9.]+' "$lib" | sort -u | head -1); \
    echo "=== all_reduce_perf 实际加载: $lib ($ver) ==="; \
    case "$ver" in \
      2.2[7-9].*|2.[3-9][0-9].*) ;; \
      *) echo "FATAL: 运行期解析到 ${ver:-unknown} ($lib), 需 >=2.27" >&2; exit 1;; \
    esac

WORKDIR /workspace
