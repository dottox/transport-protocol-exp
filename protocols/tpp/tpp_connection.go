package tpp

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"slices"
	"sync"
	"time"
)

// TPPconnection represents a TPP connection with a remote address.
type TPPconnection struct {
	RemoteAddr *net.UDPAddr
	SeqNum     uint32 // the next sequence number to use
	AckNum     uint32 // the highest continuous sequence number we have received

	Acknowledged bool
	Greeted      bool

	// tracks packets that have been sent but not ACKed
	PacketsNotACKed map[uint32]*tppPacket // SeqNum -> Packet
	PacketsLock     sync.Mutex

	// tracks received fragments
	FragmentBuffer map[uint32]*FragmentGroup // StartSeq -> FragmentGroup
	FragmentLock   sync.Mutex

	ProcessedPackets  chan []byte
	NextPacketNeedACK bool
	Handler           *TPPhandler
	closeChan         chan struct{}

	payload uint

	wg sync.WaitGroup
}

// represents a group of fragments
// exists until all fragments are received and sended to the app layer
type FragmentGroup struct {
	StartSeq uint32
	EndSeq   uint32
	Packets  map[uint32][]byte // SeqNum -> Raw Data (without metadata)
	LastSeen time.Time
}

// createConnection creates a new TPPconnection instance.
// and starts its background workers: retransmission checker and fragment timeout checker.
func createConnection(addr *net.UDPAddr, handler *TPPhandler, seqNum, ackNum uint32) *TPPconnection {
	conn := &TPPconnection{
		RemoteAddr:       addr,
		Handler:          handler,
		SeqNum:           seqNum,
		AckNum:           ackNum,
		Acknowledged:     false,
		Greeted:          false,
		PacketsNotACKed:  make(map[uint32]*tppPacket),
		FragmentBuffer:   make(map[uint32]*FragmentGroup),
		ProcessedPackets: make(chan []byte, 100),
		closeChan:        make(chan struct{}),
	}

	conn.wg.Add(2)

	go func() {
		defer conn.wg.Done()
		conn.checkRetransmission()
	}()

	go func() {
		defer conn.wg.Done()
		conn.checkFragmentTimeouts()
	}()

	return conn
}

// processPacket processes an incoming TPP packet.
func (c *TPPconnection) processPacket(packet *tppPacket, addr *net.UDPAddr) {
	remoteIP := addr.IP.String()
	flags := packet.getFlags()

	// --- ACK Handling ---
	if slices.Contains(flags, FLAG_ACK) {
		c.PacketsLock.Lock()
		// remove all packets with SeqNum <= AckNum
		for seq := range c.PacketsNotACKed {
			if seq <= packet.Header.AckNum {
				log.Printf("(ACK) [TPP] ACK received for packet %d\n", seq)
				delete(c.PacketsNotACKed, seq)
			}
		}
		c.PacketsLock.Unlock()
	}

	// --- END handling ---
	if slices.Contains(flags, FLAG_END) {
		log.Printf("[TPP] END flag received from %s. Closing connection.\n", addr)
		c.Handler.removeConnection(remoteIP)
		return
	}

	// --- Handshake Handling ---
	if !c.Greeted {
		if len(flags) == 2 && slices.Contains(flags, FLAG_HI) && slices.Contains(flags, FLAG_ACK) {
			if packet.Header.AckNum != c.SeqNum {
				log.Printf("(ERROR) [TPP] Invalid ACK number from %s during handshake\n", addr)
				c.sendPacket([]byte{}, FLAG_END)
				c.Handler.removeConnection(remoteIP)
				return
			}
			c.Greeted = true
			c.Acknowledged = true
			c.newAckSeqNum(packet.Header.SeqNum, c.SeqNum+1)
			c.sendPacket([]byte{}, FLAG_ACK)
			return
		}
	}
	if !c.Acknowledged {
		if len(flags) == 1 && slices.Contains(flags, FLAG_ACK) {
			c.Acknowledged = true
			return
		}
	}

	// --- NACK Handling ---
	if slices.Contains(flags, FLAG_NACK) {
		log.Printf("[TPP] NACK received. Checking dropped packets...\n")
		data := packet.Data

		// must be multiple of 4 bytes
		// data contains list of dropped SeqNums
		if len(data)%4 == 0 {
			for i := 0; i < len(data); i += 4 {
				droppedSeq := binary.BigEndian.Uint32(data[i : i+4])
				c.PacketsLock.Lock()

				// if we have the packet, resend it immediately
				if pkt, exists := c.PacketsNotACKed[droppedSeq]; exists {
					log.Printf("[TPP] FAST RETRANSMIT: Resending packet %d\n", droppedSeq)

					// set RSD flag to indicate retransmission due to NACK
					pkt.Header.Flags |= FLAG_RSD
					pkt.Header.Checksum = pkt.calculateChecksum()
					bytes, _ := pkt.marshal()
					c.Handler.UDPconn.WriteToUDP(bytes, c.RemoteAddr)
					pkt.LastSent = time.Now()
				}
				c.PacketsLock.Unlock()
			}
		}
	}

	// --- DATA Processing ---
	if slices.Contains(flags, FLAG_DATA) {

		length := len(packet.Data)
		c.payload = uint(length / 3)

		// to avoid duplicates packets
		if packet.Header.SeqNum <= c.AckNum {
			return
		}

		// --- Fragments Handling ---
		if slices.Contains(flags, FLAG_FRG) {
			// extract group metadata
			startSeq, endSeq, actualData := unwrapFragmentData(packet.Data)

			// creates or update the group
			c.FragmentLock.Lock()
			group, exists := c.FragmentBuffer[startSeq]
			if !exists {
				group = &FragmentGroup{
					StartSeq: startSeq,
					EndSeq:   endSeq,
					Packets:  make(map[uint32][]byte),
					LastSeen: time.Now(),
				}
				c.FragmentBuffer[startSeq] = group
			}

			group.Packets[packet.Header.SeqNum] = actualData
			group.LastSeen = time.Now()

			// check if the group is completed
			expectedCount := int(endSeq - startSeq + 1)
			if len(group.Packets) == expectedCount {
				log.Printf("[TPP] Fragment group %d-%d complete. Reassembling.\n", startSeq, endSeq)

				// put together the data
				var fullData []byte
				for i := startSeq; i <= endSeq; i++ {
					fullData = append(fullData, group.Packets[i]...)
				}

				// send to app layer
				c.ProcessedPackets <- fullData

				// response with a single ACK for the last SeqNum
				c.newAckSeqNum(endSeq, c.SeqNum+1)
				c.sendPacket([]byte{}, FLAG_ACK)

				// remove the group from buffer
				delete(c.FragmentBuffer, startSeq)
			} else {
				// if we have the end of the packet but the group is not complete
				// send NACK for missing packets
				if packet.Header.SeqNum == endSeq {
					c.sendNackForGroup(group)
				}
			}
			c.FragmentLock.Unlock()
			return
		}

		// Normal Single Packet Data
		log.Printf("[TPP] Received Single DATA packet from %s\n", addr)
		c.newAckSeqNum(packet.Header.SeqNum, c.SeqNum)
		c.NextPacketNeedACK = true
		c.ProcessedPackets <- packet.Data

		// check if the ACK for the packet was sent after 1 second,
		// if not, send it
		go func(targetSeq uint32) {
			time.Sleep(300 * time.Millisecond)
			if c.NextPacketNeedACK && c.SeqNum == targetSeq {
				log.Printf("[TPP] TIMEOUT ENDED: Sending delayed ACK for packet %d\n", targetSeq)
				c.newAckSeqNum(c.AckNum, c.SeqNum)
				c.NextPacketNeedACK = false
				c.sendPacket([]byte{}, FLAG_ACK)
			}
		}(c.SeqNum)
	}
}

// sendNackForGroup sends a NACK for missing packets in a fragment group.
func (c *TPPconnection) sendNackForGroup(group *FragmentGroup) {
	// find missing packets
	var missing []uint32
	for i := group.StartSeq; i <= group.EndSeq; i++ {
		if _, ok := group.Packets[i]; !ok {
			missing = append(missing, i)
		}
	}

	// send NACK if there are missing packets
	if len(missing) > 0 {
		log.Printf("[TPP] Detected gaps in group %d-%d. Missing: %v. Sending NACK.\n", group.StartSeq, group.EndSeq, missing)

		// serializing SeqNums of missing packets
		payload := make([]byte, len(missing)*4)
		for idx, seq := range missing {
			binary.BigEndian.PutUint32(payload[idx*4:(idx+1)*4], seq)
		}
		c.sendPacket(payload, FLAG_NACK)
	}
}

// newAckSeqNum updates the connection's AckNum and SeqNum.
func (c *TPPconnection) newAckSeqNum(ack, seq uint32) {
	c.AckNum = ack
	c.SeqNum = seq
}

// sendPacket sends a TPP packet with the given data and flags.
func (c *TPPconnection) sendPacket(data []byte, flags ...uint) {

	// include ACK flag if needed
	if c.NextPacketNeedACK {
		if !slices.Contains(flags, FLAG_ACK) {
			flags = append(flags, FLAG_ACK)
		}
		c.NextPacketNeedACK = false
	}

	packet := c.Handler.createPacket(createFlagsByte(flags), c.SeqNum, c.AckNum, data)
	packet.LastSent = time.Now()

	bytes, err := packet.marshal()
	if err != nil {
		log.Printf("(ERROR) [TPP] Error marshaling TPP packet: %v\n", err)
		return
	}

	log.Printf("[TPP] Sending packet to %s\n\t- flags %v\tsN: %d aN: %d\n\t- data: %s\n", c.RemoteAddr.String(), fmt.Sprintf("%08b", createFlagsByte(flags)), packet.Header.SeqNum, packet.Header.AckNum, string(packet.Data))

	// send packet over UDP
	c.Handler.UDPconn.WriteToUDP(bytes, c.RemoteAddr)

	isOnlyAck := len(flags) == 1 && slices.Contains(flags, FLAG_ACK)
	isRst := slices.Contains(flags, FLAG_RST)
	isNack := slices.Contains(flags, FLAG_NACK)

	// store packet for potential retransmission
	// excluded ACK-only, RST and NACK packets
	if !isOnlyAck && !isRst && !isNack {
		log.Printf("[TPP] Saved packet %d for potential retransmission\n", packet.Header.SeqNum)
		c.PacketsLock.Lock()
		c.PacketsNotACKed[packet.Header.SeqNum] = packet
		c.PacketsLock.Unlock()
	}
}

// SendMultiPacket sends data in multiple packets with extra overhead
func (c *TPPconnection) SendMultiPacket(data []byte) {

	const maxPayload = 1400                                   // safe size under MTU - headers
	const dataPerPacket = maxPayload - FRAGMENT_METADATA_SIZE // reserve 8 bytes for start/end

	// calculate number of packets needed
	totalLen := len(data)
	numPackets := (totalLen + dataPerPacket - 1) / dataPerPacket

	log.Printf("(MULTIPACKET) [TPP] Preparing to send MultiPacket of size %d bytes in %d parts\n", len(data), numPackets)

	// calculate start and end sequence numbers
	startSeq := c.SeqNum + 1 // use next seq num
	endSeq := startSeq + uint32(numPackets) - 1

	log.Printf("[TPP] Sending MultiPacket. Group: %d to %d. Total Bytes: %d\n", startSeq, endSeq, totalLen)

	// iterate over data and send packets
	for i := 0; i < totalLen; i += dataPerPacket {
		c.SeqNum++ // incrementing before so matchs calculated startSeq

		// if we reach the end, truncate to the end
		limit := i + dataPerPacket
		if limit > totalLen {
			limit = totalLen
		}

		chunk := data[i:limit]

		wrappedData := wrapFragmentData(startSeq, endSeq, chunk)

		// sending with FRG flag
		c.sendPacket(wrappedData, FLAG_DATA, FLAG_FRG)
	}
}

// background worker for connection
// checks for packets that need retransmission
func (c *TPPconnection) checkRetransmission() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeChan:
			return
		case <-ticker.C: // every 50 milliseconds
			c.PacketsLock.Lock()
			now := time.Now()

			for _, pkt := range c.PacketsNotACKed {
				// if more than 300 ms has passed since last sent, retransmit
				if now.Sub(pkt.LastSent) > 300*time.Millisecond {
					log.Printf("[TPP] Timeout retransmitting packet %d\n", pkt.Header.SeqNum)
					pkt.LastSent = now
					bytes, _ := pkt.marshal()
					c.Handler.UDPconn.WriteToUDP(bytes, c.RemoteAddr)
				}
			}
			c.PacketsLock.Unlock()
		}
	}
}

// background worker for connection
// checks for fragment groups that have timed out
func (c *TPPconnection) checkFragmentTimeouts() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.closeChan:
			return
		case <-ticker.C: // every 20 milliseconds
			c.FragmentLock.Lock()
			now := time.Now()

			// iterate over the map
			for _, group := range c.FragmentBuffer {
				// if we haven't seen a packet for this group
				// in 100 milliseconds, send NACK
				if now.Sub(group.LastSeen) > 100*time.Millisecond {
					c.sendNackForGroup(group)
					group.LastSeen = now // reset timer
				}
			}
			c.FragmentLock.Unlock()
		}
	}
}

// Write sends data over the TPP connection.
func (c *TPPconnection) Write(data []byte) {
	if len(data) < 1400 {
		c.SeqNum++ // Increment for single packet
		c.sendPacket(data, FLAG_DATA)
	} else {
		c.SendMultiPacket(data)
	}
}

// Read receives processed data from the TPP connection.
func (c *TPPconnection) Read() []byte {
	return <-c.ProcessedPackets
}
