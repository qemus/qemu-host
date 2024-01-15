<h1 align="center">QEMU Host<br />
<div align="center">
<img src="https://github.com/qemus/qemu-docker/raw/master/.github/logo.png" title="Logo" style="max-width:100%;" width="128" />
</div>
<div align="center">

[![Build]][build_url]
[![Version]][tag_url]
[![Size]][tag_url]
[![Pulls]][hub_url]

</div></h1>

Tool for communicating with a QEMU Guest Agent daemon.

It is used to exchange information between the host and guest, and to execute commands in the guest.

## Usage

Via `docker-compose.yml`

```yaml
version: "3"
services:
  qemu:
    container_name: qemu
    image: qemux/qemu-host
    restart: on-failure
```

Via `docker run`

```bash
docker run -it --rm qemux/qemu-host
```

### Background

Ultimately the QEMU Guest Agent aims to provide access to a system-level agent via standard QMP commands.

This support is targeted for a future QAPI-based rework of QMP, however, so currently, for QEMU 0.15, the guest agent is exposed to the host via a separate QEMU chardev device (generally, a unix socket) that communicates with the agent using the QMP wire protocol (minus the negotiation) over a virtio-serial or isa-serial channel to the guest. Assuming the agent will be listening inside the guest using the virtio-serial device at /dev/virtio-ports/org.qemu.guest_agent.0 (the default), the corresponding host-side QEMU invocation would be something:

```
  qemu \
  ...
  -chardev socket,path=/tmp/qga.sock,server=on,wait=off,id=qga0 \
  -device virtio-serial \
  -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0
```

Commands would be then be issued by connecting to /tmp/qga.sock, writing the QMP-formatted guest agent command, reading the QMP-formatted response, then disconnecting from the socket. (It's not strictly necessary to disconnect after a command, but should be done to allow sharing of the guest agent with multiple client when exposing it as a standalone service in this fashion. When guest agent passthrough support is added to QMP, QEMU/QMP will handle arbitration between multiple clients).

When QAPI-based QMP is available (somewhere around the QEMU 0.16 timeframe), a different host-side invocation that doesn't involve access to the guest agent outside of QMP will be used. Something like:

```
  qemu \
  ...
  -chardev qga_proxy,id=qga0 \
  -device virtio-serial \
  -device virtserialport,chardev=qga0,name=org.qemu.guest_agent.0
  -qmp tcp:localhost:4444,server
```

Currently this is planned to be done as a pseudo-chardev that only QEMU/QMP sees or interacts with, but the ultimate implementation may vary to some degree. The net effect should the same however: guest agent commands will be exposed in the same manner as QMP commands using the same QMP server, and communication with the agent will be handled by QEMU, transparently to the client.

The current list of supported RPCs is documented in qemu.git/qapi-schema-guest.json.

[build_url]: https://github.com/qemus/qemu-host/
[hub_url]: https://hub.docker.com/r/qemux/qemu-host/
[tag_url]: https://hub.docker.com/r/qemux/qemu-host/tags

[Build]: https://github.com/qemus/qemu-host/actions/workflows/build.yml/badge.svg
[Size]: https://img.shields.io/docker/image-size/qemux/qemu-host/latest?color=066da5&label=size
[Pulls]: https://img.shields.io/docker/pulls/qemux/qemu-host.svg?style=flat&label=pulls&logo=docker
[Version]: https://img.shields.io/docker/v/qemux/qemu-host/latest?arch=amd64&sort=semver&color=066da5
