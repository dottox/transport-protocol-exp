package client

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dottox/tinder-packet-protocol/protocols/tap"
	"github.com/dottox/tinder-packet-protocol/protocols/tpp"
)

type Client struct {
	Name  string
	Specs Specifications

	ClientIP   string
	ClientPort int

	ServerIP   string // IP of the connected server
	ServerPort string

	IsConnected bool // indicates if the client is connected to a server

	TimePoint time.Time // used for ping measurement

	TPPhandler *tpp.TPPhandler

	Matches []*Client // list of matched clients
}

type Specifications struct {
	CPU          string
	RAM          string
	GraphicsCard string
}

// Encode encodes the Client struct into a byte slice.
func (c Client) Encode() []byte {
	return []byte(fmt.Sprintf("%s;%s;%s;%s", c.Name, c.Specs.CPU, c.Specs.RAM, c.Specs.GraphicsCard))
}

// DecodeClient decodes a byte slice into a Client struct.
func DecodeClient(data []byte) (*Client, error) {
	parts := strings.Split(string(data), ";")
	if len(parts) != 4 {
		return nil, fmt.Errorf("[DECODE] data no pertenece a la estructura de un cliente")
	}

	return &Client{
		Name: parts[0],
		Specs: Specifications{
			CPU:          parts[1],
			RAM:          parts[2],
			GraphicsCard: parts[3],
		},
	}, nil
}

// NewClient creates a new Client instance
func NewClient(name string, specs Specifications) *Client {
	return &Client{
		Name:       name,
		Specs:      specs,
		TPPhandler: tpp.NewTPPhandler(),
	}
}

// Start starts the client's TPP handler
func (c *Client) Start() {
	err := c.TPPhandler.Start("0") // Port "0" lets the OS choose an available port
	if err != nil {
		log.Fatalf("Error starting TPP handler: %v", err)
	}

	// handles matches connections from peer
	go func() {
		for {
			conn := c.TPPhandler.NextConnection()
			go func(conn *tpp.TPPconnection) {
				for {
					data := conn.Read()
					log.Printf("Received data from %s: %d bytes\n", conn.RemoteAddr.String(), len(data))

					// #####################################
					// Only case we receive data here
					// is when a match sends us an image
					// #####################################

					// save connection info
					remoteIP := conn.RemoteAddr.IP.String()
					remotePort := conn.RemoteAddr.Port

					// check if the sender is already in matches
					finded := false
					for _, c := range c.Matches {
						if c.ClientIP == remoteIP && c.ClientPort == remotePort {
							log.Printf("Connection from match %s (%s:%d)\n", c.Name, remoteIP, remotePort)
							finded = true
						}
					}

					// if we don't have the match, add it
					if !finded {
						c.Matches = append(c.Matches, &Client{
							Name: "unknown",
							Specs: Specifications{
								CPU:          "unknown",
								RAM:          "unknown",
								GraphicsCard: "unknown",
							},
							ClientIP:   remoteIP,
							ClientPort: remotePort,
						})
					}

					// save image to file
					timestamp := time.Now().Unix()
					filename := fmt.Sprintf("received_image_%s_%d.jpg", remoteIP, timestamp)
					err := os.WriteFile(filename, data, 0644)
					if err != nil {
						log.Printf("Error saving received image: %v\n", err)
					} else {
						log.Printf("Image received from %s saved as %s\n", conn.RemoteAddr.String(), filename)
					}

				}
			}(conn)
		}
	}()
}

// connectionMenu handles user input to connect to a server
func (c *Client) connectionMenu(r *bufio.Reader) error {
	// ask for the server's ip
	fmt.Printf("Enter server IP: ")
	ip, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	ip = ip[:len(ip)-1] // Remove newline character

	// ask for the server's port
	fmt.Printf("Enter server Port: ")
	port, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	port = port[:len(port)-1] // Remove newline character

	// dial server
	err = c.TPPhandler.Dial(ip, port)
	if err != nil {
		return err
	}

	// wait for the connection to be established
	time.Sleep(100 * time.Millisecond)

	c.IsConnected = true
	c.ServerIP = ip
	c.ServerPort = port

	// ping server to verify connection
	c.pingServer()

	// register client in the server
	c.registerInServer()

	return nil
}

// registerInServer allow the client to register so they can find a match
// Specification of the client is send to the server
func (c *Client) registerInServer() {
	conn, exist := c.TPPhandler.GetConnection(c.ServerIP, c.ServerPort)
	if !exist {
		log.Printf("No existing connection to server %s\n", c.ServerIP)
		return
	}

	// create a packet with HI and Client's details
	registerPacket, err := tap.CreatePacket(c.Encode(), tap.FLAG_HELLO, tap.FLAG_DATA).Marshal()
	if err != nil {
		log.Printf("Error marshaling TAP packet: %v\n", err)
		return
	}

	// send the packet
	conn.Write(registerPacket)

	// read the response
	response := conn.Read()
	tapPacket, err := tap.Unmarshal(response)
	if err != nil {
		log.Printf("Error unmarshaling TAP packet from server: %v\n", err)
		return
	}

	// check for the server's response
	flags := tapPacket.GetFlags()
	if slices.Contains(flags, tap.FLAG_HELLO) {
		log.Printf("Message from server: %s\n", string(tapPacket.Data))
	} else if slices.Contains(flags, tap.FLAG_ERR) && slices.Contains(flags, tap.FLAG_DATA) {
		log.Printf("Error... message from server: %s\n", string(tapPacket.Data))
	} else {
		log.Printf("Error... Unexpected response from server %s during registration\n", c.ServerIP)
	}
}

// matchMenu shows the menu to find any matches
func (c *Client) matchMenu() error {
	if !c.IsConnected {
		return fmt.Errorf("not connected to any server")
	}

	conn, exist := c.TPPhandler.GetConnection(c.ServerIP, c.ServerPort)
	if !exist {
		return fmt.Errorf("no existing connection to server %s", c.ServerIP)
	}

	// create a packet with FIND flag
	findPacket, err := tap.CreatePacket([]byte{}, tap.FLAG_FIND).Marshal()
	if err != nil {
		return fmt.Errorf("error marshaling TAP packet: %v", err)
	}

	// send the packet
	conn.Write(findPacket)

	fmt.Printf("Searching for a match...\n")

	// read the repsonse
	response := conn.Read()
	tapPacket, err := tap.Unmarshal(response)
	if err != nil {
		return fmt.Errorf("error unmarshaling TAP packet from server: %v", err)
	}

	// check for the server's response
	flags := tapPacket.GetFlags()
	if slices.Contains(flags, tap.FLAG_FIND) && slices.Contains(flags, tap.FLAG_DATA) {
		parts := strings.Split(string(tapPacket.Data), "|")
		if len(parts) < 3 {
			return fmt.Errorf("invalid match data received from server")
		}

		// extract match IP and port
		matchIP := parts[0]
		matchPort := parts[1]
		matchData := strings.Join(parts[2:], "|")

		fmt.Printf("Match found at %s:%s\n", matchIP, matchPort)

		// decode match client data
		matchClient, err := DecodeClient([]byte(matchData))
		if err != nil {
			return fmt.Errorf("error decoding match client data: %v", err)
		}
		matchClient.ClientIP = matchIP
		matchClient.ClientPort, err = strconv.Atoi(matchPort)
		if err != nil {
			return fmt.Errorf("error converting port to integer: %v", err)
		}

		fmt.Printf("Name: %s\n", matchClient.Name)
		fmt.Printf("CPU: %s\n", matchClient.Specs.CPU)
		fmt.Printf("RAM: %s\n", matchClient.Specs.RAM)
		fmt.Printf("Graphics Card: %s\n", matchClient.Specs.GraphicsCard)

		// add to matches
		c.Matches = append(c.Matches, matchClient)

		c.TPPhandler.Dial(matchIP, matchPort)
	} else if slices.Contains(flags, tap.FLAG_ERR) && slices.Contains(flags, tap.FLAG_DATA) {
		fmt.Printf("Error... message from server: %s\n", string(tapPacket.Data))
	}

	return nil
}

// SendImageMenu lets send an image to any match
func (c *Client) sendImageMenu(r *bufio.Reader) error {
	if len(c.Matches) == 0 {
		return fmt.Errorf("No matches available. Please find a match first.\n")
	}

	fmt.Printf("Matches:\n")
	for i, match := range c.Matches {
		fmt.Printf("%d. %s (%s:%d)\n", i+1, match.Name, match.ClientIP, match.ClientPort)
	}

	// Getting which match
	fmt.Printf("Select match number to send image to: ")
	choiceStr, _ := r.ReadString('\n')
	choiceStr = choiceStr[:len(choiceStr)-1] // Remove newline character

	choice, err := strconv.Atoi(choiceStr)
	if err != nil || choice < 1 || choice > len(c.Matches) {
		return fmt.Errorf("Invalid choice while selecting match.\n")
	}
	selectedMatch := c.Matches[choice-1]

	// Getting image
	fmt.Printf("Enter image file path: ")
	imagePath, _ := r.ReadString('\n')
	imagePath = imagePath[:len(imagePath)-1] // Remove newline character

	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return fmt.Errorf("Error reading image file: %v\n", err)
	}

	matchIP := selectedMatch.ClientIP
	matchPort := strconv.Itoa(selectedMatch.ClientPort)

	// Establish connection to the match if not already connected
	_, exist := c.TPPhandler.GetConnection(matchIP, matchPort)
	if !exist {
		err = c.TPPhandler.Dial(matchIP, matchPort)
		if err != nil {
			return fmt.Errorf("Error connecting to match %s:%s: %v\n", matchIP, matchPort, err)
		}
		// wait for the connection to be established
		time.Sleep(400 * time.Millisecond)
	}

	// Send image
	matchConn, _ := c.TPPhandler.GetConnection(matchIP, matchPort)
	matchConn.Write(imageData)

	fmt.Printf("Image sent to %s successfully.\n", selectedMatch.Name)
	return nil
}

// Menu displays the main menu and handles user input
func (c *Client) Menu() {
	reader := bufio.NewReader(os.Stdin)

	for {
		cmd := exec.Command("clear")
		cmd.Stdout = os.Stdout
		err := cmd.Run()
		if err != nil {
			log.Printf("error clearing screen: %v\n", err)
		}

		fmt.Printf("1. Connect to server\n")
		fmt.Printf("2. Find match\n")
		fmt.Printf("3. Send image\n")
		fmt.Printf("4. View specifications\n")
		fmt.Printf("5. Logger on/off\n")
		fmt.Printf("6. Exit\n")
		fmt.Printf("\n> ")

		input, _ := reader.ReadString('\n')
		switch input {
		case "1\n": // connect to server
			err := c.connectionMenu(reader)
			if err != nil {
				fmt.Printf("Error connecting to server: %v\n", err)
			} else {
				fmt.Printf("Connected to server %s\n", c.ServerIP)
			}
			reader.ReadString('\n')
		case "2\n": // find match
			err := c.matchMenu()
			if err != nil {
				fmt.Printf("Error finding match: %v\n", err)
			}
			reader.ReadString('\n')
		case "3\n": // send image
			err := c.sendImageMenu(reader)
			if err != nil {
				fmt.Printf("Error sending image: %v\n", err)
			}
			reader.ReadString('\n')
		case "4\n":
			err := c.showSpecification()
			if err != nil {
				fmt.Printf("Error printing out specification: %v\n", err)
			}
			reader.ReadString('\n')
		case "5\n":
			if log.Writer() == io.Discard {
				log.SetOutput(os.Stdout)
				fmt.Println("Logger enabled.")
			} else {
				log.SetOutput(io.Discard)
				fmt.Println("Logger disabled.")
			}
			reader.ReadString('\n')
		case "6\n":
			fmt.Println("Exiting...")
			return
		default:
			fmt.Println("Invalid option, please try again.")
		}
	}
}

// pingServer allow the client to view latency connection
func (c *Client) pingServer() {
	pingPacket := []byte("PING")
	conn, exist := c.TPPhandler.GetConnection(c.ServerIP, c.ServerPort)
	if !exist {
		log.Printf("No existing connection to server %s\n", c.ServerIP)
		return
	}

	log.Printf("Sending PING to the server %s\n", c.ServerIP)

	c.TimePoint = time.Now()
	conn.Write(pingPacket)

	packet := conn.Read()
	if !bytes.Equal(packet, []byte("PONG")) {
		log.Printf("Unexpected response from server %s: %s\n", c.ServerIP, string(packet))
		return
	}

	rtt := time.Since(c.TimePoint)
	log.Printf("Received PONG from server %s in %v\n", c.ServerIP, rtt)
}

func (c *Client) showSpecification() error {
	// check if the specification is empty
	if c.Specs.CPU != "" || c.Specs.RAM != "" || c.Specs.GraphicsCard != "" {
		return fmt.Errorf("No specification for the client.")
	}

	fmt.Printf("Client Specifications:\n")
	fmt.Printf("- Name: %s\n", c.Name)
	fmt.Printf("- CPU: %s\n", c.Specs.CPU)
	fmt.Printf("- RAM: %s\n", c.Specs.RAM)
	fmt.Printf("- Graphics Card: %s\n", c.Specs.GraphicsCard)
	if c.ClientIP != "" && c.ClientPort != 0 {
		fmt.Printf("\n- Client IP: %s\n", c.ClientIP)
		fmt.Printf("- Client Port: %d\n", c.ClientPort)
	}

	if c.IsConnected {
		fmt.Printf("\nConnected to server at %s:%s\n", c.ServerIP, c.ServerPort)
	}

	return nil
}

// Gives the name, specs, and ip/port of the Client
func (c *Client) String() string {
	return fmt.Sprintf("Name: %s, Specs: [CPU: %s, RAM: %s, GraphicsCard: %s], IP: %s, Port: %d",
		c.Name, c.Specs.CPU, c.Specs.RAM, c.Specs.GraphicsCard, c.ClientIP, c.ClientPort)
}
