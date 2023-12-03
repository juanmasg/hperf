// Copyright (c) 2015-2023 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/google/uuid"
)

var port = func() string {
	p := os.Getenv("NPERF_PORT")
	if p == "" {
		p = os.Getenv("HPERF_PORT")
		if p == "" {
			p = "9999"
		}
	}
	return p
}()

var selfDetectPort = func() string {
	if sp := os.Getenv("HPERF_SELF_PORT"); sp != "" {
		return sp
	}
	sp, err := strconv.Atoi(port)
	if err != nil {
		log.Fatal(err)
	}
	sp++
	return strconv.Itoa(sp)
}()

var uniqueStr = uuid.New().String()

var oneMB = 1024 * 1024

var (
	dataIn  uint64
	dataOut uint64
)

const dialTimeout = 1 * time.Second

func printDataOut(reportTimestamps bool) {
	for {
		time.Sleep(time.Second)
		lastDataIn := atomic.SwapUint64(&dataIn, 0)
		lastDataOut := atomic.SwapUint64(&dataOut, 0)
    if reportTimestamps{
		  log.Printf("Bandwidth:  %s/s RX  |  %s/s TX\n", humanize.Bytes(lastDataIn), humanize.Bytes(lastDataOut))
    }else{
		  fmt.Printf("Bandwidth:  %s/s RX  |  %s/s TX\n", humanize.Bytes(lastDataIn), humanize.Bytes(lastDataOut))
    }
	}
}

func handleTX(conn net.Conn, b []byte) error {
	defer conn.Close()
	for {
		n, err := conn.Write(b)
		if err != nil {
			log.Println("TX-Error", conn, err)
			return err
		}
		atomic.AddUint64(&dataOut, uint64(n))
	}
}

func handleRX(conn net.Conn) {
	defer conn.Close()
	b := make([]byte, oneMB)
	for {
		n, err := conn.Read(b)
		if err != nil {
			log.Println("RX-Error", conn, err)
			return
		}
		atomic.AddUint64(&dataIn, uint64(n))
	}
}

func runServer() {
	l, err := net.Listen("tcp", net.JoinHostPort("", port))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	for {
		// Listen for an incoming connection.
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		// Handle connections in a new goroutine.
		go handleRX(conn)
	}
}

func runClient(host string) {
	host = host + ":" + port
	b := make([]byte, oneMB)
	proc := runtime.GOMAXPROCS(0)
	if proc < 16 {
		proc = 16 // 16 TCP connections is more than enough to saturate a 100G link.
	}
  log.Printf("Number of client procs: %d\n", proc)
	var wg sync.WaitGroup
	for i := 0; i < proc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var conn net.Conn
			var err error
			// Establish the connection.
			for {
				conn, err = net.Dial("tcp", host)
				if err != nil {
					log.Println("Dial-Error", conn, err)
					time.Sleep(dialTimeout)
					continue
				} else {
					break
				}
			}
			// Use the connection.
			if err := handleTX(conn, b); err != nil {
				panic(err)
			}
		}()
	}
	wg.Wait()
}

func main() {

  var maxTime = flag.Int64("t", 60, "Max time in seconds")
  var reportTimestamps = flag.Bool("T", false, "Report timestamps")

	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal("provide a list of hostnames or IP addresses")
	}

	hostMap := make(map[string]struct{}, flag.NArg())
	for _, host := range flag.Args() {
    log.Printf("Host %s\n", host)
		if _, ok := hostMap[host]; ok {
			log.Fatalln("duplicate arguments found, please make sure all arguments are unique")
		}
		hostMap[host] = struct{}{}
	}

	s := &http.Server{
		Addr:           ":" + selfDetectPort,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		http.HandleFunc("/"+uniqueStr, func(w http.ResponseWriter, req *http.Request) {})
		s.ListenAndServe()
	}()
	log.Println("Starting HTTP service to skip self.. waiting for 10secs for services to be ready")
	time.Sleep(time.Second * 10)

	go runServer()
	go printDataOut(*reportTimestamps)
	for host := range hostMap {
		resp, err := http.Get("http://" + host + ":" + selfDetectPort + "/" + uniqueStr)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close() // close the connection.
			s.Close()         // close the server as we are done.
			log.Println("HTTP service closed after successful skip...")
			continue
		}
		go runClient(host)
	}
	//time.Sleep(time.Hour * 72)
  time.Sleep(time.Second * time.Duration(*maxTime))
}
