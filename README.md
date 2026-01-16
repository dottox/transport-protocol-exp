# TPP: Tinder Packet Protocol

**TPP** is a custom, reliable transport layer protocol built on top of UDP in Go. To demonstrate its capabilities, this project includes a "Client Matcher" application (mimicking a dating app for computer specifications) that uses TPP for server communication and Peer-to-Peer (P2P) image transfer.

## 🧠 Project Overview

The project is divided into two distinct layers:

1.  **The TPP Protocol (Transport Layer):** A custom implementation providing TCP-like reliability over UDP.
2.  **The Application Layer:** A Client/Server architecture where clients register their hardware specs, find matches, and send images directly to peers.

### 📦 TPP Protocol Features
Located in `tpp/`, this package handles the low-level networking:
*   **Reliability:** Implements Sequencing (`SeqNum`/`AckNum`) and Handshakes (`HI`/`ACK`) to ensure ordered delivery.
*   **Packet Structure:** Custom 20-byte header containing Identifier, Flags, Sequence/Ack numbers, and Checksums (CRC32).
*   **Fragmentation:** Automatically splits large payloads (like images) into smaller chunks (`FLAG_FRG`) and reassembles them at the destination using `FragmentGroups`.
*   **Retransmission:** Detects packet loss and handles retransmission requests via `NACK` or timeouts.
*   **Multiplexing:** The `TPPhandler` manages multiple concurrent connections over a single UDP socket.

### 💻 Application: The Matcher
*   **Server:** Listens for connections, stores active clients in memory, and facilitates matchmaking logic.
*   **Client:** 
    *   Connects to the server to register hardware specs (CPU, RAM, GPU).
    *   Requests a "Match" (peer).
    *   **P2P Transfer:** Once matched, initiates a direct TPP connection to the peer to send image files reliably.


### 🚀 Roadmap / Future Features

The following features are planned to improve protocol efficiency and robustness. Contributions are welcome:

*   **Enhanced Connection Termination**: Improve the handling of END and RST flags for graceful shutdowns, and better utilization of the RSD (Resend) flag.
*   **Bitmask NACKs**: Replace the current uint32 slice sequence in Negative Acknowledgments (NACK) with a bitmask to reduce packet overhead.
*   **Max Retries**: Implement a hard limit on retransmission attempts before dropping a connection to prevent infinite loops.
*   **Graceful Shutdown**: Capture SIGINT (Ctrl+C) signals to send an END packet to peers before closing the application.
*   **Flow Control**: Update SendMultiPacket to limit the number of concurrent packets sent (Windowing) to prevent overflowing the receiver's buffer.
*   **Dynamic RTT (Jacobson's Algorithm)**: Implement dynamic Retransmission Timeouts (RTO) by measuring Round Trip Time (RTT) of previous packets, allowing the protocol to adapt to network congestion and delay variations.
*   **Memory Optimization**: Use sync.Pool to reuse byte buffers during packet reading/marshalling to reduce Garbage Collector pressure.
