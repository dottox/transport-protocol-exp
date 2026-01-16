package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"

	"github.com/dottox/tinder-packet-protocol/client"
	"github.com/dottox/tinder-packet-protocol/server"
)

func mainClient() {
	randomInt := rand.IntN(10)

	client := client.NewClient("Client"+fmt.Sprint(randomInt), client.Specifications{
		CPU:          "Intel i" + fmt.Sprint(5+randomInt),
		RAM:          "16GB",
		GraphicsCard: "NVIDIA GTX 1660",
	})

	go client.Start()

	client.Menu()
}

func mainServer() {
	server := server.NewServer("12321")
	server.Start()
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	text, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("Error reading from stdin: %v\n", err)
	}

	switch text {
	case "server\n":
		mainServer()
	case "client\n":
		mainClient()
	default:
		println("Please specify 'server' or 'client'")
	}
}
