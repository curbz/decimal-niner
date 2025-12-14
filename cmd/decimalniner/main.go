package main

import (
    "bytes"
    "encoding/binary"
    "fmt"
    "log"
    "net"
    "time"
)

// --- Configuration Constants ---
const (
    XPlaneIP   = "127.0.0.1" // Change this if X-Plane is on a different machine
    XPlanePort = "49000"     // Standard X-Plane UDP port
    ListenPort = "49005"     // Port for Go application to listen for data
)

// The dataref we want to read (e.g., indicated airspeed)
const datarefToRequest = "sim/flightmodel/position/indicated_airspeed"
// A unique ID we assign to this dataref. X-Plane sends this back.
const datarefID = 1

// --- Main Functions ---

func main() {
    // 1. Setup Listener
    conn, err := setupListener()
    if err != nil {
        log.Fatalf("Error setting up UDP listener: %v", err)
    }
    defer conn.Close()
    log.Printf("Listening for X-Plane data on UDP port %s...", ListenPort)

    // 2. Start Data Receiver Goroutine
    go receiveData(conn)

    // 3. Send RREF Request
    sendRREFRequest()

    // 4. Keep Main Thread Alive
    // Wait for the receiver to do its job.
    log.Println("Sent RREF request. Press Ctrl+C to stop.")
    select {} // Block forever
}

// --- Network Setup and Request Function ---

// setupListener creates a UDP socket for receiving data from X-Plane.
func setupListener() (*net.UDPConn, error) {
    addr, err := net.ResolveUDPAddr("udp", ":"+ListenPort)
    if err != nil {
        return nil, err
    }
    return net.ListenUDP("udp", addr)
}

// sendRREFRequest sends the RREF packet to X-Plane to start the data stream.
// 
func sendRREFRequest() {
    // Format: "RREF\0" (5 bytes) + Frequency (4 bytes) + Dataref Name (500 bytes max)
    
    // 1. Resolve X-Plane address
    xplaneAddr, err := net.ResolveUDPAddr("udp", XPlaneIP+":"+XPlanePort)
    if err != nil {
        log.Fatalf("Could not resolve X-Plane address: %v", err)
    }

    // 2. Prepare the request packet buffer
    // The X-Plane protocol expects a specific structure.
    buf := new(bytes.Buffer)

    // A. Header: "RREF\0" (5 bytes)
    buf.Write([]byte("RREF\x00")) 

    // B. Frequency: 4 bytes (e.g., 20.0 Hz)
    // We send data 20 times per second. 
    // The ID for the dataref is prepended to the frequency for the RREF request.
    // X-Plane expects the dataref ID (int32) and frequency (float32) together.
    
    // The RREF packet structure is tricky. X-Plane expects:
    // RREF\0 (5 bytes)
    // ID (int32, e.g., 1)
    // Frequency (float32, e.g., 20.0)
    // Name (dataref string + null padding to 500 bytes)
    
    // Write ID (1)
    if err := binary.Write(buf, binary.LittleEndian, int32(datarefID)); err != nil {
        log.Fatal(err)
    }

    // Write Frequency (20.0)
    if err := binary.Write(buf, binary.LittleEndian, float32(20.0)); err != nil {
        log.Fatal(err)
    }
    
    // C. Dataref Name: Write the dataref string and pad it to the expected length (500 bytes)
    nameBytes := []byte(datarefToRequest)
    // Ensure we don't overflow the buffer if the name is too long, though 500 is generous
    if len(nameBytes) > 500 {
        nameBytes = nameBytes[:500] 
    }
    buf.Write(nameBytes)

    // Padding with null bytes (0) to reach 500 bytes for the dataref string space
    paddingSize := 500 - len(nameBytes) 
    buf.Write(bytes.Repeat([]byte{0}, paddingSize))

    // 3. Send the UDP packet
    conn, err := net.DialUDP("udp", nil, xplaneAddr)
    if err != nil {
        log.Fatalf("Failed to dial X-Plane: %v", err)
    }
    defer conn.Close()

    log.Printf("Sending RREF request for '%s' to %s...", datarefToRequest, xplaneAddr.String())
    _, err = conn.Write(buf.Bytes())
    if err != nil {
        log.Fatalf("Error sending RREF request: %v", err)
    }
}

// --- Data Receiver Function ---

// receiveData continuously reads and parses incoming UDP packets from X-Plane.
func receiveData(conn *net.UDPConn) {
    buffer := make([]byte, 1500) // Standard UDP max size

    for {
        n, _, err := conn.ReadFromUDP(buffer)
        if err != nil {
            log.Printf("Error reading UDP: %v", err)
            continue
        }

        // X-Plane UDP packets start with "DATA\0" (5 bytes)
        if n >= 5 && string(buffer[:4]) == "DATA" {
            parseDataPacket(buffer[:n])
        } else {
            // Ignore other packets or log an unknown packet
            log.Printf("Received %d bytes of unknown data.", n)
        }
    }
}

// parseDataPacket processes the "DATA" packet structure.
// 
func parseDataPacket(data []byte) {
    // The DATA packet has a header ("DATA\0", 5 bytes), followed by 8-byte chunks.
    // Each chunk contains:
    // 1. ID (4 bytes, int32) - The unique ID we assigned in the RREF request.
    // 2. Value (4 bytes, float32) - The actual dataref value.
    
    if len(data) < 5 {
        return // Too small
    }
    
    // Data starts after the "DATA\0" header (index 5)
    data = data[5:]

    // Process all 8-byte chunks in the rest of the packet
    for i := 0; i < len(data); i += 8 {
        if i+8 > len(data) {
            break // Incomplete chunk
        }

        chunk := data[i : i+8]
        
        // Read ID (int32)
        var id int32
        err := binary.Read(bytes.NewReader(chunk[:4]), binary.LittleEndian, &id)
        if err != nil {
            log.Printf("Error reading ID: %v", err)
            continue
        }

        // Read Value (float32)
        var value float32
        err = binary.Read(bytes.NewReader(chunk[4:]), binary.LittleEndian, &value)
        if err != nil {
            log.Printf("Error reading value: %v", err)
            continue
        }

        // Check if the ID matches the dataref we requested
        if id == datarefID {
            // Success! Print the value.
            fmt.Printf("[%s] %s: %.2f kts\n", 
                time.Now().Format("15:04:05.000"), 
                datarefToRequest, 
                value)
        } else {
            // This is for another dataref (if you requested multiple)
            log.Printf("Received data for unexpected ID %d: %.2f", id, value)
        }
    }
}