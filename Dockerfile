FROM --platform=$BUILDPLATFORM golang:1.21-alpine as builder

COPY src/ /src/qemu-host/
WORKDIR /src/qemu-host

RUN go mod download

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -installsuffix cgo -o /src/qemu-host/main .

FROM scratch

COPY --from=builder /src/qemu-host/main /qemu-host.bin

ENTRYPOINT ["/qemu-host.bin"]
