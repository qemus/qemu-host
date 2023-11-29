FROM --platform=$BUILDPLATFORM golang:1.21-alpine as builder

COPY src/ /src/qemu-host/
WORKDIR /src/qemu-host

RUN go mod download

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -installsuffix cgo -o /src/qemu-host/main .

FROM scratch

COPY --from=builder /src/qemu-host/main /qemu-host.bin

ARG VERSION_ARG="0.0"
ENV VERSION=$VERSION_ARG

LABEL org.opencontainers.image.title="QEMU Host"
LABEL org.opencontainers.image.description="Host for communicating with a QEMU Agent"

ENTRYPOINT ["/qemu-host.bin"]
