package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/dottox/tinder-packet-protocol/protocols/tpp"
)

// add rules for simulating network conditions:
// sudo tc qdisc add dev lo root netem delay 20ms 5ms loss 0.1%
// sudo tc qdisc del dev lo root

const (
	PayloadSize = 1 * 1024 * 1024 // 1 MB
	Iterations  = 10              // rounds
	Host        = "127.0.0.1"

	BasePort = 9000 // base port, one round per port increment
)

type Result struct {
	Protocol string
	Duration time.Duration
	Speed    float64 // MB/s
}

func main() {
	// deactivate logs
	log.SetOutput(io.Discard)

	// generate paylod
	fmt.Printf("Generating payload of %.2f MB...\n", float64(PayloadSize)/(1024*1024))
	data := make([]byte, PayloadSize)
	rand.Read(data)

	fmt.Printf("--- Starting multi-round benchmark (%d rounds) ---\n", Iterations)
	fmt.Println("----------------------------------------------------------")

	// --- TCP LOOPS ---
	var tcpResults []Result
	fmt.Println("-- TCP rounds... --")
	for i := 1; i <= Iterations; i++ {
		// dynamic ports: 9000 + i (e.g. 9001, 9002...)
		port := strconv.Itoa(BasePort + i)

		duration := runTCPBenchmark(data, port)

		speed := (float64(PayloadSize) / (1024 * 1024)) / duration.Seconds()
		tcpResults = append(tcpResults, Result{"TCP", duration, speed})
		fmt.Printf("   Round %d (Port %s): %.2f MB/s (%v)\n", i, port, speed, duration)
		time.Sleep(100 * time.Millisecond)
	}
	printProtocolSummary("TCP", tcpResults)

	fmt.Println("----------------------------------------------------------")

	// --- TPP LOOPS ---
	var tppResults []Result
	fmt.Println("-- TPP rounds... --")
	for i := 1; i <= Iterations; i++ {
		// dynamic ports: 9000 + 100 + i (ej: 9101, 9102...) para no chocar con TCP
		port := strconv.Itoa(BasePort + 100 + i)

		duration := runTPPBenchmark(data, port)

		speed := (float64(PayloadSize) / (1024 * 1024)) / duration.Seconds()
		tppResults = append(tppResults, Result{"TPP", duration, speed})
		fmt.Printf("   Round %d (Port %s): %.2f MB/s (%v)\n", i, port, speed, duration)
		time.Sleep(100 * time.Millisecond)
	}
	printProtocolSummary("TPP", tppResults)

	printResults(tcpResults, tppResults)
}

func printProtocolSummary(protocol string, results []Result) {
	var totalTime time.Duration
	var minTime = results[0].Duration
	var maxTime = results[0].Duration

	for _, r := range results {
		totalTime += r.Duration
		if r.Duration < minTime {
			minTime = r.Duration
		}
		if r.Duration > maxTime {
			maxTime = r.Duration
		}
	}

	avgDuration := totalTime / time.Duration(len(results))
	avgSpeed := (float64(PayloadSize) / (1024 * 1024)) / avgDuration.Seconds()

	fmt.Printf("\n[Summary %s]\n", protocol)
	fmt.Printf("   Average:  %.2f MB/s (%v)\n", avgSpeed, avgDuration)
	fmt.Printf("   Best:     %v\n", minTime)
	fmt.Printf("   Worse:      %v\n", maxTime)
}

func printResults(tcpRes, tppRes []Result) {
	var avgTcp, avgTpp time.Duration
	for _, r := range tcpRes {
		avgTcp += r.Duration
	}
	for _, r := range tppRes {
		avgTpp += r.Duration
	}
	avgTcp /= time.Duration(len(tcpRes))
	avgTpp /= time.Duration(len(tppRes))

	fmt.Println("\n==========================================================")
	fmt.Println("                   BENCHMARK RESULTS                      ")
	fmt.Println("==========================================================")

	ratio := avgTpp.Seconds() / avgTcp.Seconds()

	if avgTcp < avgTpp {
		fmt.Printf("TCP is %.2fx more faster than TPP.\n", ratio)
	} else {
		fmt.Printf("¡¡¡TPP IS %.2fx MORE FASTER THAN TCP.!!\n", avgTcp.Seconds()/avgTpp.Seconds())
	}
}

func runTPPBenchmark(payload []byte, port string) time.Duration {
	doneChan := make(chan struct{})

	// --- start server
	server := tpp.NewTPPhandler()
	if err := server.Start(port); err != nil {
		fmt.Printf("(Start Error) ")
		return 10 * time.Second
	}

	// function for server to accept connection and read
	go func() {
		conn := server.NextConnection()
		if conn != nil {
			_ = conn.Read() // Lee todo el payload
		}
		close(doneChan)
	}()

	// --- start client
	client := tpp.NewTPPhandler()
	client.Start("0")

	// dial to server
	if err := client.Dial(Host, port); err != nil {
		server.Stop()
		client.Stop()
		fmt.Printf("(Dial Error) ")
		return 10 * time.Second
	}

	// small sleep to ensure connection is established
	time.Sleep(50 * time.Millisecond)

	// get the connection
	conn, exists := client.GetConnection(Host, port)
	if !exists {
		server.Stop()
		client.Stop()
		return 10 * time.Second
	}

	// send payload
	start := time.Now()
	conn.Write(payload)

	var dur time.Duration

	// wait the end or after 15 seconds
	select {
	case <-doneChan:
		dur = time.Since(start)
	case <-time.After(15 * time.Second):
		fmt.Print("(Timeout TPP) ")
		dur = 15 * time.Second
	}

	// wait goroutines to finish
	time.Sleep(200 * time.Millisecond)

	client.Stop()
	server.Stop()

	return dur
}

func runTCPBenchmark(payload []byte, port string) time.Duration {
	doneChan := make(chan struct{})

	// start TCP server
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Printf("(TCP Listen Error: %v) ", err)
		return 10 * time.Second
	}
	defer listener.Close()

	// server goroutine to accept and read
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		totalRead := 0
		buf := make([]byte, 4096)
		for totalRead < len(payload) {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			totalRead += n
		}
		close(doneChan)
	}()

	// small sleep to ensure server is ready
	time.Sleep(50 * time.Millisecond)

	// start TCP client
	conn, err := net.Dial("tcp", Host+":"+port)
	if err != nil {
		fmt.Printf("(TCP Dial Error) ")
		return 10 * time.Second
	}
	defer conn.Close()

	// send payload
	start := time.Now()
	_, err = conn.Write(payload)
	if err != nil {
		fmt.Printf("(TCP Write Error) ")
		return 10 * time.Second
	}

	<-doneChan
	return time.Since(start)
}
