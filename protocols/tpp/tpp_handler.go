package tpp

import (
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TPPhandler struct {
	Connections map[string]*TPPconnection // ipAddr:Port -> Connection
	ConnLock    sync.Mutex

	UDPconn *net.UDPConn

	ProcessedPackets  chan []byte
	ConnectionsBuffer chan *TPPconnection
}

// NewTPPhandler creates a new TPP handler.
func NewTPPhandler() *TPPhandler {
	return &TPPhandler{
		Connections:       make(map[string]*TPPconnection),
		ProcessedPackets:  make(chan []byte, 100),
		ConnectionsBuffer: make(chan *TPPconnection),
	}
}

// GetConnection retrieves a connection by IP address.
func (h *TPPhandler) GetConnection(ip, port string) (*TPPconnection, bool) {
	h.ConnLock.Lock()
	defer h.ConnLock.Unlock()

	conn, ok := h.Connections[ip+":"+port]
	return conn, ok
}

// saveConnection saves a connection in the handler.
func (h *TPPhandler) saveConnection(ip, port string, conn *TPPconnection) {
	h.ConnLock.Lock()
	defer h.ConnLock.Unlock()

	h.Connections[ip+":"+port] = conn
}

// removeConnection removes a connection from the handler.
func (h *TPPhandler) removeConnection(ip string) {
	h.ConnLock.Lock()
	defer h.ConnLock.Unlock()

	delete(h.Connections, ip)
}

// Start opens the UDP port and starts the reading loop in the background.
func (h *TPPhandler) Start(port string) error {
	// parses the address
	udpAddr, err := net.ResolveUDPAddr("udp", ":"+port)
	if err != nil {
		return fmt.Errorf("error resolving UDP address: %v", err)
	}

	// opens the UDP socket to listen on
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("error listening on UDP port: %v", err)
	}
	h.UDPconn = conn

	// Start reading in a separate goroutine so we don't block
	go h.readLoop()

	log.Printf("[TPP] Listening on %s\n", udpAddr)
	return nil
}

func (h *TPPhandler) Stop() {
	h.ConnLock.Lock()

	// send close signal to all connections
	for _, conn := range h.Connections {
		select {
		case <-conn.closeChan:
		default:
			close(conn.closeChan)
		}
	}
	h.ConnLock.Unlock() // unlock so the conns can finish

	// making sure that all connections finish
	h.ConnLock.Lock()
	for _, conn := range h.Connections {
		conn.wg.Wait()
	}
	h.ConnLock.Unlock()

	// close UDP connection
	if h.UDPconn != nil {
		h.UDPconn.Close()
	}
}

// readLoop handles incoming UDP packets indefinitely
func (h *TPPhandler) readLoop() {
	defer h.UDPconn.Close()

	for {
		// 1500 bytes for the MTU
		buf := make([]byte, 1500)

		// read from UDP connection
		n, remoteAddr, err := h.UDPconn.ReadFromUDP(buf)
		if err != nil {
			// if the error is due to closed connection, just return silently
			if strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			fmt.Printf("Error reading: %v\n", err)
			return
		}

		// process the packet in a separate goroutine
		go func(data []byte, addr *net.UDPAddr) {
			h.processPacket(data, addr)
		}(buf[:n], remoteAddr)
	}
}

// Dial initiates a connection to a remote TPP peer.
func (h *TPPhandler) Dial(address, port string) error {
	// Resolve remote address
	remoteUDPAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%s", address, port))
	if err != nil {
		return fmt.Errorf("error resolving remote address: %v", err)
	}

	remoteIP := remoteUDPAddr.IP.String()
	remotePort := strconv.Itoa(remoteUDPAddr.Port)

	// Check if we are already connected
	if _, exists := h.GetConnection(remoteIP, remotePort); exists {
		return fmt.Errorf("connection to %s already exists", remoteIP)
	}

	// Create the connection
	newConn := createConnection(remoteUDPAddr, h, 0, 0)
	h.saveConnection(remoteIP, remotePort, newConn)

	// Send the initial HI
	log.Printf("[TPP] Dialing %s... Sending HI", remoteUDPAddr)
	newConn.sendPacket([]byte{}, FLAG_HI)

	return nil
}

// processPacket processes an incoming TPP packet.
// If no connection exists for the packet, it handles the handshake process.
// If a connection exists, it delegates the packet to the corresponding connection.
func (h *TPPhandler) processPacket(data []byte, addr *net.UDPAddr) {
	tppPkt, err := unmarshal(data)
	if err != nil {
		log.Printf("(ERROR) [TPP] Error unmarshaling TPP packet from %s: %v\n", addr, err)
		return
	}

	log.Printf("[TPP] Received from %s: \n\t- flags: %s\t sN: %d \t aN: %d\n\t- data: %s\n", addr, fmt.Sprintf("%08b", tppPkt.Header.Flags), tppPkt.Header.SeqNum, tppPkt.Header.AckNum, tppPkt.Data)

	remoteIP := addr.IP.String()
	remotePort := strconv.Itoa(addr.Port)
	tppConn, existConn := h.GetConnection(remoteIP, remotePort)

	// Validate TPP identifier
	if !tppPkt.isIdentifierValid() {
		log.Printf("(ERROR) [TPP] Invalid identifier number in TPP packet from %s\n", addr)
		if existConn {
			tppConn.newAckSeqNum(tppConn.AckNum, tppConn.SeqNum+1)
			tppConn.sendPacket([]byte{}, FLAG_RST)
		} else {
			h.sendPacketWithoutConn(addr, []byte{}, FLAG_RST)
		}
		return
	}
	// Validate checksum
	if !tppPkt.validChecksum() {
		log.Printf("(ERROR) [TPP] Invalid checksum in TPP packet from %s\n", addr)
		if existConn {
			tppConn.newAckSeqNum(tppConn.AckNum, tppConn.SeqNum+1)
			tppConn.sendPacket([]byte{}, FLAG_RSD)
		} else {
			h.sendPacketWithoutConn(addr, []byte{}, FLAG_RSD)
		}
		return
	}

	// #---- HANDSHAKE PROCESS ----#
	// if no connection exists
	if !existConn {
		log.Printf("(INFO) [TPP] No existing connection for %s. Processing handshake...\n", addr)
		h.processHandshake(tppPkt, addr)
	} else { // if connection exists
		log.Printf("(INFO) [TPP] Existing connection for %s. Delegating packet to connection...\n", addr)
		tppConn.processPacket(tppPkt, addr)
	}
}

// processHandshake handles TPP handshake packets when no connection exists.
func (h *TPPhandler) processHandshake(packet *tppPacket, addr *net.UDPAddr) {
	remoteIP := addr.IP.String()
	remotePort := strconv.Itoa(addr.Port)
	flags := packet.getFlags()

	// check for HI flag only
	if len(flags) == 1 && slices.Contains(flags, FLAG_HI) {
		newConn := createConnection(addr, h, 0, packet.Header.SeqNum)
		newConn.Greeted = true

		// waits 1 second for the connection
		// to be accepted by the application layer
		ticker := time.NewTicker(100 * time.Millisecond)
		select {
		case <-ticker.C:
			// timeout
			log.Printf("(ERROR) [TPP] Timeout waiting for ACK from %s during handshake\n", addr)
			h.sendPacketWithoutConn(addr, []byte{}, FLAG_RST)
			return
		case h.ConnectionsBuffer <- newConn:
			// accepted connection
			ticker.Stop()
		}

		// save connection and send HI+ACK
		h.saveConnection(remoteIP, remotePort, newConn)
		log.Printf("[TPP] New TPP connection handshake with %s\n", addr)
		newConn.sendPacket([]byte{}, FLAG_ACK, FLAG_HI)
		return
	} else {
		log.Printf("(ERROR) [TPP] No existing connection for %s and FLAG_HI not set\n", addr)
		h.sendPacketWithoutConn(addr, []byte{}, FLAG_RST)
		return
	}
}

// createPacket creates a TPP packet.
func (h *TPPhandler) createPacket(flags uint8, seqNum, ackNum uint32, data []byte) *tppPacket {
	packet := &tppPacket{
		Header: tppHeader{
			Identifier: IDENTIFIER_NUMBER,
			SeqNum:     seqNum,
			AckNum:     ackNum,
			Flags:      flags,
			Length:     uint32(len(data)),
		},
		Data: data,
	}

	packet.Header.Checksum = packet.calculateChecksum()

	return packet
}

// sendPacketWithoutConn sends a TPP packet to an address without a connection.
// data must be an unmarshaled TPP packet.
func (h *TPPhandler) sendPacketWithoutConn(addr *net.UDPAddr, data []byte, flags ...uint) {
	// create new packet conectionless
	packet := h.createPacket(createFlagsByte(flags), 0, 0, []byte{})

	// transform packet into data
	data, err := packet.marshal()
	if err != nil {
		log.Printf("(ERROR) [TPP] Error marshaling connection-less TPP packet to %s: %v\n", addr, err)
		return
	}

	// send data through UDP connection
	_, err = h.UDPconn.WriteToUDP(data, addr)
	if err != nil {
		log.Printf("(ERROR) [TPP] Error sending connection-less TPP packet to %s: %v\n", addr, err)
		return
	}
}

// NextConnection return a new TPP connection when is available.
func (h *TPPhandler) NextConnection() *TPPconnection {
	return <-h.ConnectionsBuffer
}
