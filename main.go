package main

import (
	"container/list"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"golang.org/x/net/context"
)

type ForwardedPort struct {
	listener net.Listener
	conns    *list.List
	to       string
	mu       sync.Mutex
	wg       sync.WaitGroup
}

func ForwardPort(port, to string) (*ForwardedPort, error) {
	l, err := net.Listen("tcp", port)
	if err != nil {
		return nil, err
	}
	pf := &ForwardedPort{
		listener: l,
		conns:    list.New(),
		to:       to,
	}
	go pf.listen()
	return pf, nil
}

func (pf *ForwardedPort) listen() {
	defer func() {
		// Close all active connections.
		pf.mu.Lock()
		defer pf.mu.Unlock()

		for item := pf.conns.Front(); item != nil; item = item.Next() {
			conn := item.Value.(net.Conn)
			conn.Close()
		}
		pf.conns = list.New()
	}()

	for {
		conn, err := pf.listener.Accept()
		if err != nil {
			// Assume the accept failed because we are closing the connection.
			return
		}
		go pf.serve(conn)
	}
}

func (pf *ForwardedPort) serve(conn net.Conn) {
	pf.mu.Lock()
	item := pf.conns.PushBack(conn)
	pf.mu.Unlock()

	defer func() {
		pf.mu.Lock()
		defer pf.mu.Unlock()
		pf.conns.Remove(item)
	}()
	defer conn.Close()

	// Dial a new connection to the to address.
	addr, err := net.ResolveTCPAddr("tcp", pf.to)
	if err != nil {
		fmt.Printf("unable to resolve tcp address %s: %s\n", pf.to, err)
		return
	}
	to, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		fmt.Printf("unable to dial to %s: %s\n", pf.to, err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		io.Copy(to, conn)
		to.CloseWrite()
		wg.Done()
	}()
	go func() {
		io.Copy(conn, to)
		to.CloseRead()
		wg.Done()
	}()
	wg.Wait()
}

func (pf *ForwardedPort) Close() error {
	if err := pf.listener.Close(); err != nil {
		return err
	}
	pf.wg.Wait()
	return nil
}

type PortMapper struct {
	client      *docker.Client
	forwardings map[string][]*ForwardedPort
	toHost      string
}

func NewPortMapper(client *docker.Client) *PortMapper {
	u, _ := docker.ParseHostURL(client.DaemonHost())
	host, _, _ := net.SplitHostPort(u.Host)
	return &PortMapper{
		client:      client,
		forwardings: make(map[string][]*ForwardedPort),
		toHost:      host,
	}
}

func (pm *PortMapper) startContainer(id string) {
	attrs, err := pm.client.ContainerInspect(context.Background(), id)
	if err != nil {
		fmt.Printf("unable to inspect container %s, skipping\n", id)
		return
	}

	ports := make([]*ForwardedPort, 0, 1)
	for port, bindings := range attrs.HostConfig.PortBindings {
		if port.Proto() != "tcp" {
			continue
		}
		for _, binding := range bindings {
			fp, err := ForwardPort(":"+binding.HostPort, pm.toHost+":"+binding.HostPort)
			if err != nil {
				fmt.Printf("skipping port forwarding for %s: %s\n", binding.HostPort, err)
				continue
			}
			ports = append(ports, fp)
		}
	}
	pm.forwardings[id] = ports
}

func (pm *PortMapper) stopContainer(id string) {
	if ports, ok := pm.forwardings[id]; ok {
		for _, fp := range ports {
			fp.Close()
		}
		delete(pm.forwardings, id)
	}
}

func (pm *PortMapper) Run() error {
	ctx := context.Background()
	// Start listening for events before inspecting the existing containers so we don't miss any messages.
	messages, errs := pm.client.Events(ctx, types.EventsOptions{
		Filters: filters.NewArgs(
			filters.Arg("event", "start"),
			filters.Arg("event", "stop"),
			filters.Arg("type", "container"),
		),
	})

	// Inspect the existing containers and forward any ports.
	if containers, err := pm.client.ContainerList(context.Background(), types.ContainerListOptions{Quiet: true}); err != nil {
		fmt.Printf("unable to inspect existing containers: %s\n", err)
	} else {
		for _, container := range containers {
			pm.startContainer(container.ID)
		}
	}

	// Process incoming messages.
	for {
		select {
		case event := <-messages:
			switch event.Action {
			case "start":
				pm.startContainer(event.Actor.ID)
			case "stop":
				pm.stopContainer(event.Actor.ID)
			}
		case err := <-errs:
			return err
		}
	}
}

func main() {
	client, err := docker.NewEnvClient()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.Close()

	// Verify the docker client works.
	if _, err := client.Ping(context.Background()); err != nil {
		fmt.Printf("Unable to connect to the docker daemon: %s\n", err)
		return
	}

	portMapper := NewPortMapper(client)
	if err := portMapper.Run(); err != nil {
		fmt.Println(err)
		return
	}
}
