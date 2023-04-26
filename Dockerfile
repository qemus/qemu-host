FROM golang:alpine as builder

COPY / /src/qemu-host/
WORKDIR /src/qemu-host

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /src/qemu-host/main .

FROM scratch

COPY --from=builder /src/qemu-host/main /qemu-host.bin

ARG DATE_ARG=""
ARG BUILD_ARG=0
ARG VERSION_ARG="0.0"
ENV VERSION=$VERSION_ARG

LABEL org.opencontainers.image.created=${DATE_ARG}
LABEL org.opencontainers.image.revision=${BUILD_ARG}
LABEL org.opencontainers.image.version=${VERSION_ARG}
LABEL org.opencontainers.image.url=https://hub.docker.com/r/qemux/qemu-host/
LABEL org.opencontainers.image.source=https://github.com/qemu-tools/qemu-host/

ENTRYPOINT ["/qemu-host.bin"]
