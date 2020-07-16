package main

import (
	"bufio"
	"fmt"
	"github.com/hashicorp/mdns"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	usage := func() {
		fmt.Printf("Usage: %s [name]\n", os.Args[0])
	}

	if len(os.Args) != 2 {
		usage()
		os.Exit(1)
	}
	name := strings.TrimSpace(os.Args[1])
	if len(name) < 2 {
		usage()
		os.Exit(1)
	}

	// Silence hashicorp/mdns log
	// If this was anything but a toy project, I would do something else about
	// this logging
	log.SetOutput(ioutil.Discard)

	var done int32 = 0
	exit := make(chan bool)

	listener, err := net.Listen("tcp", "127.0.0.1:")
	if err != nil {
		panic(err)
	}
	addr := listener.Addr().String()

	port, err := parsePort(addr)
	if err != nil {
		panic(err)
	}

	// Start mdns server
	mdnsServer, err := setupMDNS([]string{listener.Addr().String(), name})
	if err != nil {
		panic(err)
	}

	// Start mdns client
	entriesCh := make(chan *mdns.ServiceEntry, 100)
	go func() {
		for entry := range entriesCh {
			if len(entry.InfoFields) < 2 || entry.InfoFields[1] != name {
				continue
			}

			remoteAddr := entry.InfoFields[0]
			remotePort, err := parsePort(remoteAddr)
			if err != nil {
				fmt.Println("failed to parse port from", remoteAddr)
				continue
			}

			// Instance with lowest port number should setup connection.
			// The instance may have discovered itself if port is the same
			if remotePort <= port {
				continue
			}

			conn, err := net.Dial("tcp4", remoteAddr)
			if err != nil {
				fmt.Println("Failed to connect to:", remoteAddr)
				continue
			}

			// If this is the right connection, we can close down our mdns client, server and tcp server
			if atomic.CompareAndSwapInt32(&done, 0, 1) {
				listener.Close()
				close(entriesCh)
				mdnsServer.Shutdown()
			}

			setupPipes(conn, exit)
			break
		}
	}()

	// Poll every 5 seconds (depending on how long to lookup takes...)
	go func() {
		for atomic.LoadInt32(&done) == 0 {
			mdns.Lookup("_gopaste._tcp", entriesCh)
			time.Sleep(2 * time.Second)
		}
	}()

	// Handle tcp conections
	for {
		conn, err := listener.Accept()
		if err != nil {
			if atomic.LoadInt32(&done) == 1 {
				break
			}
			panic(err)
		}

		// If this is the right connection, we can close down our mdns client, server and tcp server
		if atomic.CompareAndSwapInt32(&done, 0, 1) {
			close(entriesCh)
			mdnsServer.Shutdown()
		}

		setupPipes(conn, exit)
		break
	}
	<-exit
}

func setupMDNS(info []string) (*mdns.Server, error) {
	// Setup our service export
	host, _ := os.Hostname()
	service, err := mdns.NewMDNSService(host, "_gopaste._tcp", "", "", 8000, nil, info)
	if err != nil {
		return nil, err
	}

	// Create the mDNS server, defer shutdown
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, err
	}
	return server, err
}

func parsePort(address string) (int, error) {
	parts := strings.Split(address, ":")
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	return port, nil
}

func setupPipes(conn net.Conn, exit chan bool) {
	// Writer
	go func() {
		writer := bufio.NewWriter(conn)
		writer.ReadFrom(os.Stdin)
		exit <- true
	}()

	// Reader
	go func() {
		reader := bufio.NewReader(conn)
		reader.WriteTo(os.Stdout)
		exit <- true
	}()
}
