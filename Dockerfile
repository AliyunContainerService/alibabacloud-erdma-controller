# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Copy the Go Modules manifests and cache dependencies
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go source code and build the binaries
COPY cmd/ ./cmd/
COPY api/ ./api/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager ./cmd/controller/main.go && \
    go build -a -o agent ./cmd/agent/main.go && \
    go build -a -o smcr_init ./cmd/smcr_init/main.go

# Use distroless as minimal base image to package the manager binary
FROM gcr.io/distroless/static:nonroot AS controller
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]

FROM alibaba-cloud-linux-3-registry.cn-hangzhou.cr.aliyuncs.com/alinux3/alinux3 AS smcr_init
RUN sed -i 's/mirrors.cloud.aliyuncs.com/mirrors.aliyun.com/g' /etc/yum.repos.d/*; yum install -y smc-tools && yum clean all && rm -rf /var/cache/* /var/lib/dnf/history* /var/lib/rpm/rpm.sqlite
COPY --from=builder /workspace/smcr_init /usr/local/bin/smcr_init
ENTRYPOINT ["/usr/local/bin/smcr_init"]

FROM alibaba-cloud-linux-3-registry.cn-hangzhou.cr.aliyuncs.com/alinux3/alinux3 AS agent
RUN sed -i 's/mirrors.cloud.aliyuncs.com/mirrors.aliyun.com/g' /etc/yum.repos.d/*; yum install -y smc-tools procps-ng && yum clean all && rm -rf /var/cache/* /var/lib/dnf/history* /var/lib/rpm/rpm.sqlite
COPY --from=builder /workspace/agent /usr/local/bin/agent
COPY --from=builder /workspace/smcr_init /usr/local/bin/smcr_init
ENTRYPOINT ["/usr/local/bin/agent"]