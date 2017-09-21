# Docker Machine Proxy

The Docker Machine Proxy is for automatically forwarding TCP
connections to a Docker Machine VM.

On some platforms, Docker does not run natively and needs to be run
using a VM. Docker Machine is used for these. While this works, there
are some times where you want to contact localhost on the host machine
and communicate with the exposed ports in the VM from Docker.

This program connects to the Docker Daemon. For any started containers,
it opens ports on the host machine and proxies those connections to the
VM instance. Whenever a container starts or stops, it listens or closes
the relevant ports.

## Usage

Simply do this if your docker-machine instance is named `dev`:

```bash
$ eval (docker-machine env dev)
$ docker-machine-proxy
```

Push Ctrl+C to end the program and stop proxying any ports.
