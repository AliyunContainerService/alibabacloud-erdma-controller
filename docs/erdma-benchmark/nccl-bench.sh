#!/usr/bin/env bash
# NCCL over eRDMA 跨节点基准测试驱动脚本
#
# 前置: 已 kubectl apply -f nccl-pods.yaml 且两个 pod Ready.
# 作用: 自动建立两 pod 间 SSH 互信 -> 依次跑 单网卡 / 单网卡+GDR / 双网卡+graph 三组 all_reduce.
#
# 用法:
#   ./nccl-bench.sh                       # 默认 label app=nccl-test 自动发现 2 个 pod
#   ./nccl-bench.sh <podA> <podB>         # 指定 pod 名
#   NS=default GPUS_PER_NODE=8 ./nccl-bench.sh
#
# 依赖: kubectl 能访问集群; 镜像 registry.cn-hangzhou.aliyuncs.com/wangbs/erdma:nccl
set -euo pipefail

NS="${NS:-default}"
GPUS_PER_NODE="${GPUS_PER_NODE:-1}"          # 连通性验证用 1; 双网卡 graph 测试需设为 8
ADDR_RANGE="${ADDR_RANGE:-}"                 # eRDMA 网卡 IPv4 网段, 留空则自动探测
GRAPH_FILE="/root/nccl-graph.xml"

k() { kubectl -n "$NS" "$@"; }

# --- 1. 发现两个 pod ---
if [[ $# -ge 2 ]]; then A="$1"; B="$2"; else
  mapfile -t PODS < <(k get pods -l app=nccl-test -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  [[ ${#PODS[@]} -ge 2 ]] || { echo "找不到 2 个 app=nccl-test 的 pod, 请先 apply nccl-pods.yaml"; exit 1; }
  A="${PODS[0]}"; B="${PODS[1]}"
fi
IPA=$(k get pod "$A" -o jsonpath='{.status.podIP}')
IPB=$(k get pod "$B" -o jsonpath='{.status.podIP}')
echo "== pods: $A($IPA)  $B($IPB) =="

# --- 2. SSH 互信 + sshd ---
PUB=$(k exec "$A" -- bash -c 'mkdir -p /root/.ssh && chmod 700 /root/.ssh; [ -f /root/.ssh/id_rsa ] || ssh-keygen -t rsa -N "" -f /root/.ssh/id_rsa -q; cat /root/.ssh/id_rsa.pub')
for p in "$A" "$B"; do
  k exec "$p" -- bash -c "
    mkdir -p /root/.ssh && chmod 700 /root/.ssh
    grep -qF '$PUB' /root/.ssh/authorized_keys 2>/dev/null || echo '$PUB' >> /root/.ssh/authorized_keys
    chmod 600 /root/.ssh/authorized_keys
    printf 'Host *\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n' > /root/.ssh/config && chmod 600 /root/.ssh/config
    ssh-keygen -A >/dev/null 2>&1; mkdir -p /run/sshd
    grep -q '^PermitRootLogin yes' /etc/ssh/sshd_config || echo 'PermitRootLogin yes' >> /etc/ssh/sshd_config
    pgrep sshd >/dev/null || /usr/sbin/sshd
  "
done
k exec "$A" -- ssh -o ConnectTimeout=8 "$IPB" hostname >/dev/null && echo "SSH $A -> $B OK"

# --- 3. 自动探测 eRDMA 网卡 IPv4 网段(取 erdma_0 的 RoCEv2 IPv4 GID) ---
if [[ -z "$ADDR_RANGE" ]]; then
  IP=$(k exec "$A" -- bash -c "ibv_devinfo -d erdma_0 -v 2>/dev/null | grep -oE '::ffff:[0-9.]+' | head -1 | sed 's/::ffff://'")
  ADDR_RANGE="$(echo "$IP" | cut -d. -f1-3).0/24"
fi
echo "== NCCL_IB_ADDR_RANGE=$ADDR_RANGE  GPUS_PER_NODE=$GPUS_PER_NODE =="

# --- 4. push graph file (双网卡测试用) ---
if [[ -f "$(dirname "$0")/nccl-graph.xml" ]]; then
  for p in "$A" "$B"; do k cp "$(dirname "$0")/nccl-graph.xml" "$p:$GRAPH_FILE"; done
fi

NP=$((GPUS_PER_NODE * 2))
LDP='/usr/lib/x86_64-linux-gnu:/usr/local/cuda/lib64:/usr/lib/x86_64-linux-gnu/openmpi/lib'
COMMON=( --allow-run-as-root -np "$NP" -H "$IPA:$GPUS_PER_NODE,$IPB:$GPUS_PER_NODE"
  --mca btl_tcp_if_include eth0 --mca oob_tcp_if_include eth0
  -x NCCL_SOCKET_IFNAME=eth0
  -x NCCL_IB_GID_INDEX=-1 -x NCCL_IB_ADDR_FAMILY=AF_INET
  -x NCCL_IB_ROCE_VERSION_NUM=2 -x "NCCL_IB_ADDR_RANGE=$ADDR_RANGE"
  -x "LD_LIBRARY_PATH=$LDP" -x PATH )

run() { echo; echo "########## $1 ##########"; shift; k exec "$A" -- bash -c "export LD_LIBRARY_PATH=$LDP; mpirun $* 2>&1" | grep -E 'NET/IB|GDRDMA|GDR 0|GID [0-9]|float|Avg bus|error|WARN' | grep -v Bootstrap; }

# 单网卡(默认, 通常不走 GDR)
run "single-NIC (erdma_0)"           "${COMMON[@]}" -x NCCL_IB_HCA=erdma_0 /opt/nccl-tests/build/all_reduce_perf -b 8 -e 256M -f 2 -g 1
# 单网卡 + 强制 GDR
run "single-NIC + GDR"               "${COMMON[@]}" -x NCCL_IB_HCA=erdma_0 -x NCCL_NET_GDR_LEVEL=SYS /opt/nccl-tests/build/all_reduce_perf -b 8 -e 256M -f 2 -g 1
# 双网卡 + graph(需 GPUS_PER_NODE=8)
if [[ "$GPUS_PER_NODE" -eq 8 ]]; then
  run "dual-NIC + graph + GDR"       "${COMMON[@]}" -x NCCL_IB_HCA=erdma_0,erdma_1 -x NCCL_NET_GDR_LEVEL=SYS -x "NCCL_GRAPH_FILE=$GRAPH_FILE" /opt/nccl-tests/build/all_reduce_perf -b 256M -e 1G -f 2 -n 50 -w 20 -g 1
else
  echo; echo "(跳过双网卡测试: 需 GPUS_PER_NODE=8, 当前=$GPUS_PER_NODE)"
fi
echo; echo "== done =="
