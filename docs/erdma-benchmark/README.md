# eRDMA 单/双物理网卡 基准测试 Runbook

> 面向后续 agent / 运维的可直接执行方案：在 ACK GPU 节点上用 eRDMA 做 **verbs 层带宽（perftest）**
> 与 **NCCL 跨节点集合通信** 测试，覆盖 **单网卡** 和 **双物理网卡** 两种场景。
>
> 所有 manifest 用 `podAntiAffinity` 自动落到 2 个不同的 eRDMA 节点，无需写死 nodeName。

## 适用场景

- 验证 eRDMA 节点间 RDMA 通路是否正常（perftest）。
- 验证 NCCL 能否走 eRDMA（RoCEv2）做跨节点集合通信、是否走 GDR。
- 评估 **双物理 eRDMA 网卡** 机型的聚合带宽 / rail 使用情况。

## 前置条件

- 集群有 ≥2 个带 eRDMA 的节点（节点标签 `aliyun.accelerator/erdma: "true"`），且已部署 `alibabacloud-erdma-controller` + eRDMA agent、`aliyun/erdma` 资源可分配。
- NCCL 测试需要 GPU 节点。
- `kubectl` 可访问集群。

## 测试镜像

```text
registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
```

一个镜像同时含 perftest 和 NCCL 测试所需的一切：

| 组件 | 内容 |
|------|------|
| 基础 | Ubuntu 22.04 + CUDA 12.2 runtime + PyTorch 2.6 |
| eRDMA verbs | 阿里云官方 `rdma-core`（含 **erdma provider**）+ `perftest`（ib_send_bw / ib_write_bw / ib_write_lat）|
| NCCL | **2.27.7+cuda12.9**（支持 Blackwell sm_120；替换了基础镜像自带的 2.21.5）|
| nccl-tests | 全部 `*_perf` 二进制，用 compute_90 PTX 编译（驱动 JIT 到 sm_120）|
| 启动 | OpenMPI 4.1.2 + openssh（跨 pod mpirun）|

### 重新构建镜像

见 [`Dockerfile`](./Dockerfile)。**必须在 x86_64 机器上构建**（含 CUDA，arm 上 qemu 极慢）；建议在 **cn-hangzhou 的 ECS** 上走 VPC 内网 build+push：

```bash
# nccl-tests 源码需放在构建上下文的 ./nccl-tests 下（避免 ECS clone github 超时）：
git clone --branch v2.13.10 --depth 1 https://github.com/NVIDIA/nccl-tests.git nccl-tests && rm -rf nccl-tests/.git
docker build -t registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl .
docker push  registry-vpc.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
```

---

## 一、快速开始（NCCL 端到端）

```bash
cd docs/erdma-benchmark
kubectl apply -f nccl-pods.yaml
kubectl wait --for=condition=Ready pod -l app=nccl-test --timeout=360s

# 自动建 SSH 互信、探测 GID 网段，依次跑 单网卡 / 单网卡+GDR 三组：
./nccl-bench.sh
# 跑双物理网卡聚合（需每节点 8 GPU）：
GPUS_PER_NODE=8 ./nccl-bench.sh
```

清理：`kubectl delete -f nccl-pods.yaml`

---

## 二、perftest（verbs 层，单/双网卡）

```bash
kubectl apply -f perftest-pods.yaml
kubectl wait --for=condition=Ready pod -l app=erdma-perftest --timeout=300s
SRV=$(kubectl get pod -l app=erdma-perftest -o jsonpath='{.items[0].metadata.name}')
CLI=$(kubectl get pod -l app=erdma-perftest -o jsonpath='{.items[1].metadata.name}')
SRV_IP=$(kubectl get pod "$SRV" -o jsonpath='{.status.podIP}')
```

先确认设备与 **RoCEv2-IPv4 GID index**（每块网卡可能不同！）：

```bash
kubectl exec "$SRV" -- ibv_devinfo -d erdma_0 -v | grep -E "GID\[|state:|active_mtu"
# GID[N] 里 ::ffff:<IPv4> 那一行的 N 就是要用的 GID index
```

**单网卡** ib_write_bw：

```bash
kubectl exec "$SRV" -- sh -c 'ib_write_bw -d erdma_0 -x <GID_IDX> -F --report_gbits -D 15' &  # server
kubectl exec "$CLI" -- sh -c "ib_write_bw -d erdma_0 -x <GID_IDX> -F --report_gbits -D 15 $SRV_IP"  # client
```

**双网卡并发**（各起一个实例、不同端口，同时打流量看聚合）：

```bash
kubectl exec "$SRV" -- sh -c '
  ib_write_bw -d erdma_0 -x <GID0> -p 18515 -F --report_gbits -D 15 >/tmp/s0.log 2>&1 &
  ib_write_bw -d erdma_1 -x <GID1> -p 18516 -F --report_gbits -D 15 >/tmp/s1.log 2>&1 &
  wait' &
kubectl exec "$CLI" -- sh -c "
  ib_write_bw -d erdma_0 -x <GID0> -p 18515 -F --report_gbits -D 15 $SRV_IP >/tmp/c0.log 2>&1 &
  ib_write_bw -d erdma_1 -x <GID1> -p 18516 -F --report_gbits -D 15 $SRV_IP >/tmp/c1.log 2>&1 &
  wait; tail -n2 /tmp/c0.log /tmp/c1.log"
```

清理：`kubectl delete -f perftest-pods.yaml`

---

## 三、NCCL 跨节点（手动，理解每一步）

`nccl-bench.sh` 已把下述步骤自动化；手动跑时：

### 3.1 SSH 互信
两 pod 间建立免密 SSH 并起 sshd（mpirun 依赖）。脚本 `nccl-bench.sh` 的第 2 步即此逻辑。

### 3.2 单网卡 all_reduce（连通性）
```bash
mpirun --allow-run-as-root -np 2 -H <IPA>:1,<IPB>:1 \
  --mca btl_tcp_if_include eth0 --mca oob_tcp_if_include eth0 \
  -x NCCL_SOCKET_IFNAME=eth0 \
  -x NCCL_IB_HCA=erdma_0 -x NCCL_IB_GID_INDEX=<GID_IDX> \
  -x NCCL_DEBUG=INFO -x LD_LIBRARY_PATH -x PATH \
  /opt/nccl-tests/build/all_reduce_perf -b 8 -e 128M -f 2 -g 1
```
日志出现 `NET/IB : Using [0]erdma_0:1/RoCE` + `Using network IB` 即说明走了 eRDMA。

### 3.3 开启 GDR
默认 **不走 GDR**（`use ring ... GDR 0`），因为 GPU↔网卡 PCIe 距离超过默认阈值。加：
```bash
-x NCCL_NET_GDR_LEVEL=SYS
```
生效后通道变为 `via NET/IB/0/GDRDMA`，`GPU Direct RDMA Enabled`。

---

## 四、双物理网卡（重点）

两块 eRDMA 网卡的 **RoCEv2-IPv4 GID index 不同**（例：erdma_0=2、erdma_1=1），而 `NCCL_IB_GID_INDEX` 是全局单值 —— 硬编码搞不定两块卡。

### 4.1 GID 自动发现（解决 GID index 差异）
用 **ADDR_RANGE 按 IP 段自动选 GID**，每块网卡各自匹配自己的 IPv4 RoCEv2 GID：
```bash
-x NCCL_IB_HCA=erdma_0,erdma_1
-x NCCL_IB_GID_INDEX=-1        # 自动
-x NCCL_IB_ADDR_FAMILY=AF_INET
-x NCCL_IB_ROCE_VERSION_NUM=2
-x NCCL_IB_ADDR_RANGE=192.168.4.0/24   # 两块网卡 IPv4 所在网段
```
> 网段：取 `ibv_devinfo` 里 erdma 的 `::ffff:<IPv4>` 所在 /24。脚本会自动探测。

### 4.2 强制两块网卡并行（自定义 graph）
仅靠上面还不够：拓扑距离一致时 NCCL 会把所有 GPU 都分到第一块网卡。用 [`nccl-graph.xml`](./nccl-graph.xml) 把 **channel 0 钉到 net dev 0（erdma_0）、channel 1 钉到 net dev 0x1（erdma_1）**：
```bash
-x NCCL_GRAPH_FILE=/root/nccl-graph.xml
```
配合 **每节点 8 GPU** 运行。验证成功的标志：日志里 `via NET/IB/0/GDRDMA` 和 `via NET/IB/1/GDRDMA` 数量相当（两块网卡均衡并行），且同时出现两块网卡各自的 GID。

> `nccl-bench.sh` 里 `GPUS_PER_NODE=8` 时会自动 `kubectl cp` 该 graph 并跑这组测试。

---

## 五、关键调优参数速查

| 参数 | 作用 | 建议值 |
|------|------|--------|
| `NCCL_IB_HCA` | 选用哪些 eRDMA 设备 | `erdma_0`（单）/ `erdma_0,erdma_1`（双）|
| `NCCL_IB_GID_INDEX` | RoCE GID 索引 | `-1`（自动，配合 ADDR_RANGE）；或手写每设备的 IPv4 RoCEv2 GID |
| `NCCL_IB_ADDR_RANGE` | 按 IP 段自动选 GID | eRDMA 网卡 IPv4 网段，如 `192.168.4.0/24` |
| `NCCL_IB_ADDR_FAMILY` / `NCCL_IB_ROCE_VERSION_NUM` | 约束 GID 族/版本 | `AF_INET` / `2` |
| `NCCL_NET_GDR_LEVEL` | GDR 阈值 | `SYS`（本类机型必须显式开）|
| `NCCL_SOCKET_IFNAME` | bootstrap/控制面网卡 | `eth0`（pod overlay）|
| `NCCL_GRAPH_FILE` | 导入自定义拓扑 | `/root/nccl-graph.xml`（双网卡并行）|
| OpenMPI | 控制面绑 eth0 | `--mca btl_tcp_if_include eth0 --mca oob_tcp_if_include eth0` |

---

## 六、已知坑

1. **Blackwell / 新架构 GPU（sm_120）**：基础镜像自带的 NCCL 2.21.5 + CUDA 12.2 无 sm_120 kernel，单卡都会报 `Cuda failure 'invalid argument'`。镜像已修复：装 NCCL ≥2.27，nccl-tests 用 `compute_90` PTX 编译靠驱动 JIT。换更新架构时同理。
2. **GID index 因设备而异**：erdma_0 与 erdma_1 的 RoCEv2-IPv4 GID index 可能不同（本例 2 vs 1）。别硬编码，用 `NCCL_IB_GID_INDEX=-1 + NCCL_IB_ADDR_RANGE` 自动选。
3. **GDR 默认不开**：GPU↔网卡 PCIe 距离 8（SYS 级）超过默认 GDR 阈值，NCCL 会回退到主机内存中转。必须 `NCCL_NET_GDR_LEVEL=SYS`。NCCL 2.27 用 dmabuf，不需要 nvidia-peermem。
4. **无 NVLink 机型**：若 `nvidia-smi nvlink -s` 为空（如 RTX PRO 5000），节点内 8 卡 all_reduce 走 PCIe/主机，intra-node 成为瓶颈；此时**堆网卡也压不出双网卡聚合收益**。要看到双网卡真正加速，需 **有 NVLink + GPU/网卡 rail 对齐** 的机型。
5. **GPU 配额占满时**：`nccl-pods.yaml` 用 `privileged + NVIDIA_VISIBLE_DEVICES=all` 绕过设备插件直接访问宿主机全部 GPU（压测用）。正式调度请改回申请 `aliyun.com/gpu`。

---

## 七、参考测试结果

环境：2 节点 × (8× **NVIDIA RTX PRO 5000 72GB Blackwell**, 无 NVLink) + 2× eRDMA 网卡，驱动 CUDA 13.0。

**verbs 层（perftest）**
| 测试 | 结果 |
|------|------|
| 单网卡 ib_write_bw | ~118 Gbps（avg 108, peak 133）|
| 单网卡 ib_write_lat | typical ~16 µs |
| 双网卡并发 | erdma_0 122 + erdma_1 126 ≈ **248 Gbps** 聚合（线性叠加）|

**NCCL all_reduce（busbw，大消息）**
| 配置 | busbw |
|------|------|
| 单NIC · 2GPU · 无GDR | 14.7 GB/s |
| 单NIC · 1GPU · GDR | 20.8 GB/s |
| 单NIC · 2GPU · GDR | **26.6 GB/s** |
| 双NIC · per-rank 绑定（跨 rail，反例）| 14.2 GB/s |
| 双NIC · graph · 8GPU/node · GDR | ~18–20 GB/s（峰值 19.9 @1G）|

> 结论：graph 方案已在 NCCL 层确认**双网卡并行工作**（16+16 GDRDMA 通道、两块 GID 各就位）；但此无 NVLink 机型端到端被节点内 PCIe 卡住，双网卡聚合带宽发挥不出来 —— 属机型拓扑限制，非 eRDMA 问题。

---

## 文件清单

| 文件 | 用途 |
|------|------|
| [`Dockerfile`](./Dockerfile) | 测试镜像构建 |
| [`perftest-pods.yaml`](./perftest-pods.yaml) | verbs 层 perftest 2-pod |
| [`nccl-pods.yaml`](./nccl-pods.yaml) | NCCL 测试 2-pod（privileged + GPU + erdma）|
| [`nccl-graph.xml`](./nccl-graph.xml) | 双网卡自定义 NCCL 拓扑图 |
| [`nccl-bench.sh`](./nccl-bench.sh) | 一键驱动：SSH 互信 + 单/双网卡三组测试 |
