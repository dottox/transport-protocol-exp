package server

import (
	"bytes"
	"fmt"
	"log"
	"slices"
	"strconv"
	"sync"

	"github.com/dottox/tinder-packet-protocol/client"
	"github.com/dottox/tinder-packet-protocol/protocols/tap"
	"github.com/dottox/tinder-packet-protocol/protocols/tpp"
)

type Server struct {
	Port string

	Tpp *tpp.TPPhandler

	Clients     map[string]*client.Client // ip -> Client
	ClientsLock sync.Mutex
}

// NewServer creates a new Server instance.
// The TPPhandler is created, but not started.
func NewServer(port string) *Server {
	return &Server{
		Port:    port,
		Tpp:     tpp.NewTPPhandler(),
		Clients: make(map[string]*client.Client), // sessionID -> Client
	}
}

// Start starts the server to listen for incoming TPP connections.
func (s *Server) Start() {

	// start TPP handler
	err := s.Tpp.Start(s.Port)
	if err != nil {
		log.Fatalf("Error starting TPP handler: %v", err)
	}

	fmt.Printf("Server listening on port %s\n", s.Port)

	// accepts connections in loop and handles them in a new goroutine
	// each packet (TAP or string) is processes by their flags
	for {
		conn := s.Tpp.NextConnection()

		go func(c *tpp.TPPconnection) {
			for {
				data := c.Read()
				log.Printf("Received data from %s: %s\n", c.RemoteAddr.String(), string(data))

				// Check for PING message
				if bytes.Equal(data, []byte("PING")) {
					c.Write([]byte("PONG"))
					continue
				}

				// If the packet is TAP
				packet, err := tap.Unmarshal(data)
				if err != nil {
					log.Printf("[ERROR] (TAP) Error unmarshaling TAP packet: %v\n", err)
					continue
				}
				flags := packet.GetFlags()

				// -- Register --
				if slices.Contains(flags, tap.FLAG_HELLO) {
					log.Printf("[INFO] (SERVER) Processing new register from %s...\n", c.RemoteAddr.String())
					s.processRegister(packet, c)
				}

				// -- Find Match --
				if slices.Contains(flags, tap.FLAG_FIND) {
					log.Printf("[INFO] (SERVER) Processing new find match from %s...\n", c.RemoteAddr.String())
					s.processFindMatch(c)
				}
			}
		}(conn)
	}
}

// processRegister handles client registration packets.
func (s *Server) processRegister(packet *tap.TAPPacket, conn *tpp.TPPconnection) {
	data := packet.Data

	// decode client to register
	c, err := client.DecodeClient(data)
	if err != nil {
		log.Printf("[ERROR] (SERVER) Error decoding client data: %v\n", err)
		// send error packet to client
		tapErrorPacket, err := tap.CreatePacket([]byte("Error decoding client data"), tap.FLAG_ERR, tap.FLAG_DATA).Marshal()
		if err != nil {
			log.Printf("[ERROR] (SERVER) Error marshaling TAP error packet: %v\n", err)
			return
		}
		conn.Write(tapErrorPacket)
		return
	}

	// add ip and port
	c.ClientIP = conn.RemoteAddr.IP.String()
	c.ClientPort = conn.RemoteAddr.Port

	// save client
	s.ClientsLock.Lock()
	s.Clients[c.ClientIP+":"+strconv.Itoa(c.ClientPort)] = c
	s.ClientsLock.Unlock()

	// send HI ack
	tapHiPacket, err := tap.CreatePacket([]byte{}, tap.FLAG_HELLO).Marshal()
	conn.Write(tapHiPacket)

	log.Printf("[INFO] (SERVER) Registered new client: %s\n", c.Name)
}

// processFindMatch handles client match finding requests.
func (s *Server) processFindMatch(conn *tpp.TPPconnection) {

	// check if there are available clients to match
	if len(s.Clients) <= 1 {
		response := []byte("No matches available at the moment.")
		noFindTapPacket, err := tap.CreatePacket(response, tap.FLAG_ERR, tap.FLAG_DATA).Marshal()
		if err != nil {
			log.Printf("[ERROR] (SERVER) Error marshaling TAP find packet: %v\n", err)
			return
		}

		conn.Write(noFindTapPacket)
		log.Printf("[INFO] (SERVER) Sent find match response to %s\n", conn.RemoteAddr.String())
	} else {
		clientIp := conn.RemoteAddr.IP.String()
		clientPort := conn.RemoteAddr.Port
		clientIpPort := clientIp + ":" + strconv.Itoa(clientPort)

		// find a match (the first client that is not the requester)
		s.ClientsLock.Lock()
		var matchPeer *client.Client
		for ipPort, c := range s.Clients {
			if ipPort != clientIpPort {
				matchPeer = c
				break
			}
		}
		s.ClientsLock.Unlock()

		// get data to build response
		matchPeerInfo := matchPeer.Encode()
		ipBytes := []byte(fmt.Sprintf("%s", matchPeer.ClientIP))
		portBytes := []byte(fmt.Sprintf("%d", matchPeer.ClientPort))

		// build response: ip|port|matchData
		responseData := append(ipBytes, '|')
		responseData = append(responseData, portBytes...)
		responseData = append(responseData, '|')
		responseData = append(responseData, matchPeerInfo...)

		// create and send TAP packet
		findTapPacket, err := tap.CreatePacket(responseData, tap.FLAG_FIND, tap.FLAG_DATA).Marshal()
		if err != nil {
			log.Printf("[ERROR] (SERVER) Error marshaling TAP find packet: %v\n", err)
			return
		}

		conn.Write(findTapPacket)
		log.Printf("[INFO] (SERVER) Sent find match response to %s\n", conn.RemoteAddr.String())
	}
}
