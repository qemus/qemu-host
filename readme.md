<h1 align="center">QEMU Host
<br />
<p align="center">
<img src="https://github.com/qemu-tools/qemu-docker/raw/master/.github/logo.png" title="Logo" style="max-width:100%;" width="256" />
</p>

<div align="center">

[![build_img]][build_url]
[![gh_last_release_svg]][qemu-ghcr]
[![Docker Image Size]][qemu-ghcr]

[build_url]: https://github.com/qemu-tools/qemu-host/
[qemu-ghcr]: https://github.com/orgs/qemu-tools/packages/container/package/qemu-host

[build_img]: https://github.com/qemu-tools/qemu-host/actions/workflows/build.yml/badge.svg
[Docker Image Size]: https://ghcr-badge.egpl.dev/qemu-tools/qemu-host/size?color=SteelBlue
[gh_last_release_svg]: https://ghcr-badge.egpl.dev/qemu-tools/qemu-host/tags?n=1&label=version&color=SteelBlue

</div></h1>

Tool for communicating with a QEMU Guest Agent daemon.

It is used to exchange information between the host and guest, and to execute commands in the guest.

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
