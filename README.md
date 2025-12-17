# CryoDecoder TLV Protocol System

A high-performance, type-safe, extensible binary encoding/decoding system using a TLV (Tag-Length-Value) format.

## Features

-   **Type-Safe**: Schema-driven serialization prevents data corruption.
-   **Extensible**: Easily register custom client-defined types (structs).
-   **Performant**: Low-latency, low-overhead binary format.
-   **Robust**: Uses BOF/EOF markers for stream integrity.
-   **Simple API**: Easy to use, similar to standard Go `encoding/json`.

## Installation

```bash
go get github.com/Cryosimorgh/CryoDecoder
```

## Supported Primitive Types

The library supports all Go primitive types out of the box. Platform-dependent types (`int`, `uint`) are serialized as fixed-size (`int64`, `uint64`) for cross-platform compatibility.

-   `int`, `int8`, `int16`, `int32`, `int64`
-   `uint`, `uint8` `byte`, `uint16`, `uint32`, `uint64`, `uintptr`
-   `float32`, `float64`
-   `bool`
-   `string`
-   `complex64`, `complex128`

---

## Quick Start

A complete example of defining a schema, encoding a struct, and decoding it back.

```go
package main

import (
	"fmt"
	"log"

	"example.com/cryodecoder"
)

// 1. Define your custom data structures.
type PlayerStats struct {
	Kills    int32
	Deaths   int32
	Accuracy float64
}

type GameUpdate struct {
	PlayerID   int32
	PlayerName string
	Score      float64
	IsOnline   bool
	Stats      PlayerStats // Nested struct
}

func main() {
	// 2. Create a new registry. This is the central schema manager.
	registry := cryodecoder.NewCodecRegistry()

	// 3. Register all built-in primitive types.
	// This assigns standard tags to all supported primitives (int32, string, etc.).
	registry.RegisterPrimitives()

	// 4. Register your custom structs.
	// The library uses reflection to automatically discover fields and their types.
	// Nested structs are registered recursively.
	// This must be called for each top-level struct you intend to serialize.
	_, err := registry.RegisterStruct(GameUpdate{})
	if err != nil {
		log.Fatalf("Failed to register GameUpdate: %v", err)
	}

	// 5. Create an encoder and a decoder using the registry.
	encoder := cryodecoder.NewEncoder(registry)
	decoder := cryodecoder.NewDecoder(registry, &bytes.Buffer{}) // Using a buffer for this example

	// 6. Create an instance of your data with sample values.
	update := GameUpdate{
		PlayerID:   12345,
		PlayerName: "JaneDoe",
		Score:      9876.5,
		IsOnline:   true,
		Stats: PlayerStats{
			Kills:    150,
			Deaths:   20,
			Accuracy: 0.92,
		},
	}

	// 7. Encode the data into a binary byte slice.
	encodedData, err := encoder.Encode(update)
	if err != nil {
		log.Fatalf("Encoding failed: %v", err)
	}
	fmt.Printf("Encoded data: %x\n", encodedData)

	// 8. Decode the binary data back into a Go interface{}.
	// The decoder reads from the buffer we passed it.
	decodedValue, err := decoder.Decode()
	if err != nil {
		log.Fatalf("Decoding failed: %v", err)
	}

	// 9. Type-assert the decoded value to your original struct type.
	// This is a crucial step to use the decoded data.
	decodedUpdate, ok := decodedValue.(GameUpdate)
	if !ok {
		log.Fatalf("Decoded data is not of type GameUpdate, got %T", decodedValue)
	}

	// 10. Verify the result.
	fmt.Printf("Original: %+v\n", update)
	fmt.Printf("Decoded:  %+v\n", decodedUpdate)
}
```

---

## API Reference

### CodecRegistry

The `CodecRegistry` maps type tags to their respective `Codec` implementations.

```go
// Create a new, empty registry.
registry := cryodecoder.NewCodecRegistry()

// Register all built-in primitive codecs with standard tags.
// Must be called before registering structs that use these types.
registry.RegisterPrimitives()

// Register a custom struct and all of its nested structs.
// The library automatically discovers fields and their types via reflection.
// Returns the tag assigned to the struct and an error if registration fails.
gameUpdateTag, err := registry.RegisterStruct(GameUpdate{})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("GameUpdate struct registered with tag: %d\n", gameUpdateTag)
```

### Encoder

The `Encoder` serializes Go objects into the binary TLV format.

```go
// Create a new encoder that uses the provided registry.
encoder := cryodecoder.NewEncoder(registry)

// Encode any value that has been registered with the registry.
// Returns a byte slice containing the TLV-formatted data.
myValue := GameUpdate{ /* ... */ }
data, err := encoder.Encode(myValue)
if err != nil {
    log.Fatal(err)
}
// `data` is now ready to be written to a network connection, file, etc.
```

### Decoder

The `Decoder` deserializes a binary stream into Go objects.

```go
// The decoder needs a source to read from. This can be a network connection,
// a file, or an in-memory buffer like `bytes.Buffer`.
conn, _ := net.Dial("tcp", "localhost:8080")
decoder := cryodecoder.NewDecoder(registry, conn)

// Decode a single object from the stream.
// Blocks until an object is received or an error occurs (e.g., EOF).
value, err := decoder.Decode()
if err != nil {
    log.Fatal(err)
}

// Use a type switch to handle different possible message types.
// This is the standard pattern for handling multiple message types.
switch v := value.(type) {
case GameUpdate:
    fmt.Printf("Received GameUpdate: %+v\n", v)
case PlayerLogin:
    fmt.Printf("Received PlayerLogin: %+v\n", v)
default:
    fmt.Printf("Received unknown type: %T\n", v)
}
```

---

## Network Usage (Client/Server Example)

This example demonstrates sending and receiving data over a TCP connection.

### Server (`server.go`)

Listens for connections, decodes incoming messages, and prints them.

```go
package main

import (
	"fmt"
	"log"
	"net"
	"example.com/cryodecoder"
)

// Define all possible message structs the server can receive.
type GameUpdate struct { /* ... */ }
type PlayerLogin struct { /* ... */ }
type ChatMessage struct { /* ... */ }

func main() {
	// 1. Setup registry and register all expected message types.
	registry := cryodecoder.NewCodecRegistry()
	registry.RegisterPrimitives()
	registry.RegisterStruct(GameUpdate{})
	registry.RegisterStruct(PlayerLogin{})
	registry.RegisterStruct(ChatMessage{})

	// 2. Start a TCP listener.
	listener, err := net.Listen("tcp", ":8080")
	if err != nil { log.Fatal(err) }
	defer listener.Close()
	fmt.Println("Server listening on :8080")

	// 3. Accept connections in a loop.
	for {
		conn, err := listener.Accept()
		if err != nil { log.Println(err); continue }
		// 4. Handle each connection in a new goroutine.
		go handleConnection(conn, registry)
	}
}

func handleConnection(conn net.Conn, registry *cryodecoder.CodecRegistry) {
	defer conn.Close()
	
	// 5. Create a decoder for the connection.
	decoder := cryodecoder.NewDecoder(registry, conn)

	// 6. Decode a single message from the client.
	msg, err := decoder.Decode()
	if err != nil { log.Println(err); return }

	// 7. Use a type switch to process the message.
	switch v := msg.(type) {
	case GameUpdate:
		fmt.Printf("Received GameUpdate from %s: %+v\n", conn.RemoteAddr(), v)
	case PlayerLogin:
		fmt.Printf("Received PlayerLogin from %s: %+v\n", conn.RemoteAddr(), v)
	case ChatMessage:
		fmt.Printf("Received ChatMessage from %s: %+v\n", conn.RemoteAddr(), v)
	}
}
```

### Client (`client.go`)

Connects to the server, creates a random message, encodes it, and sends it.

```go
package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"time"
	"example.com/cryodecoder"
)

// Define all possible message structs the client can send.
// These definitions MUST match the server's.
type GameUpdate struct { /* ... */ }
type PlayerLogin struct { /* ... */ }
type ChatMessage struct { /* ... */ }

func main() {
	// 1. Setup registry and register all structs the client might send.
	registry := cryodecoder.NewCodecRegistry()
	registry.RegisterPrimitives()
	registry.RegisterStruct(GameUpdate{})
	registry.RegisterStruct(PlayerLogin{})
	registry.RegisterStruct(ChatMessage{})
	
	// 2. Create an encoder.
	encoder := cryodecoder.NewEncoder(registry)

	// 3. Generate a random message to send.
	messageToSend := generateRandomMessage()

	// 4. Encode the message.
	data, err := encoder.Encode(messageToSend)
	if err != nil { log.Fatal(err) }

	// 5. Connect to the server.
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil { log.Fatal(err) }
	defer conn.Close()

	// 6. Write the encoded data to the connection.
	_, err = conn.Write(data)
	if err != nil { log.Fatal(err) }

	fmt.Printf("Sent %T to server\n", messageToSend)
}

func generateRandomMessage() interface{} {
	// Logic to randomly create and return one of the message types.
	// ...
}
```
# Tested Example
## Client 
```go
// main implements a TCP client that sends randomly generated
// messages of different types to a server.
package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"time"

	cryodecoder "TestDecoder/CryoDecoder"
)

// --- All Possible DTO Definitions (must match server) ---

type PlayerStats struct {
	Kills    int32
	Deaths   int32
	Accuracy float64
}

type GameUpdate struct {
	PlayerID   int32
	PlayerName string
	Score      float64
	Stats      PlayerStats
}

type PlayerLogin struct {
	PlayerName string
	Timestamp  int64
}

type ChatMessage struct {
	SenderID    int32
	MessageText string
	IsGlobal    bool
}

// --- Random Data Generation ---

var randomNames = []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"}
var randomWords = []string{"hello", "world", "golang", "server", "client", "test", "message", "protocol"}

func randomString(slice []string) string {
	return slice[rand.Intn(len(slice))]
}

func randomSentence() string {
	return randomString(randomWords) + " " + randomString(randomWords) + " " + randomString(randomWords)
}

// --- Message Generation ---

func generateRandomGameUpdate() GameUpdate {
	return GameUpdate{
		PlayerID:   rand.Int31(),
		PlayerName: randomString(randomNames),
		Score:      rand.Float64() * 1000,
		Stats: PlayerStats{
			Kills:    rand.Int31() % 50,
			Deaths:   rand.Int31() % 20,
			Accuracy: rand.Float64(),
		},
	}
}

func generateRandomPlayerLogin() PlayerLogin {
	return PlayerLogin{
		PlayerName: randomString(randomNames),
		Timestamp:  time.Now().Unix(),
	}
}

func generateRandomChatMessage() ChatMessage {
	return ChatMessage{
		SenderID:    rand.Int31(),
		MessageText: randomSentence(),
		IsGlobal:    rand.Intn(2) == 1,
	}
}

// setupRegistry registers all primitives and DTOs the client might send.
func setupRegistry() *cryodecoder.CodecRegistry {
	registry := cryodecoder.NewCodecRegistry()
	registry.RegisterPrimitives()

	// Register all structs the client might send.
	_, err := registry.RegisterStruct(GameUpdate{})
	if err != nil {
		log.Fatalf("Failed to register GameUpdate: %v", err)
	}
	_, err = registry.RegisterStruct(PlayerLogin{})
	if err != nil {
		log.Fatalf("Failed to register PlayerLogin: %v", err)
	}
	_, err = registry.RegisterStruct(ChatMessage{})
	if err != nil {
		log.Fatalf("Failed to register ChatMessage: %v", err)
	}

	return registry
}

func main() {
	// Seed the random number generator.
	rand.Seed(time.Now().UnixNano())

	registry := setupRegistry()
	encoder := cryodecoder.NewEncoder(registry)

	// Define the possible message types the client can send.
	messageTypes := []interface{}{
		GameUpdate{},
		PlayerLogin{},
		ChatMessage{},
	}

	// Choose a random message type.
	chosenType := messageTypes[rand.Intn(len(messageTypes))]
	var messageToSend interface{}

	// Generate a random instance of the chosen type.
	switch chosenType.(type) {
	case GameUpdate:
		messageToSend = generateRandomGameUpdate()
	case PlayerLogin:
		messageToSend = generateRandomPlayerLogin()
	case ChatMessage:
		messageToSend = generateRandomChatMessage()
	}

	// Encode the randomly generated message.
	fmt.Printf("Encoding %T...\n", messageToSend)
	encodedData, err := encoder.Encode(messageToSend)
	if err != nil {
		log.Fatalf("Error encoding message: %v", err)
	}
	fmt.Printf("Encoding complete. Payload size: %d bytes\n", len(encodedData))

	// Connect to the server and send the data.
	conn, err := net.DialTimeout("tcp", "localhost:8080", 5*time.Second)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()
	fmt.Println("Connected to server at localhost:8080")

	_, err = conn.Write(encodedData)
	if err != nil {
		log.Fatalf("Failed to send data: %v", err)
	}

	fmt.Printf("Successfully sent %T to server.\n", messageToSend)
}

```

## Server
```go
// main implements a TCP server that can receive, decode, and print
// multiple types of binary-encoded objects.
package main

import (
	"fmt"
	"log"
	"net"

	cryodecoder "TestDecoder/CryoDecoder"
)

// --- All Possible DTO Definitions ---

// PlayerStats represents player statistics.
type PlayerStats struct {
	Kills    int32
	Deaths   int32
	Accuracy float64
}

// GameUpdate contains player score and stats.
type GameUpdate struct {
	PlayerID   int32
	PlayerName string
	Score      float64
	Stats      PlayerStats
}

// PlayerLogin is sent when a player joins the server.
type PlayerLogin struct {
	PlayerName string
	Timestamp  int64
}

// ChatMessage represents a chat message in the game.
type ChatMessage struct {
	SenderID    int32
	MessageText string
	IsGlobal    bool
}

// setupRegistry registers all primitives and all possible DTOs.
func setupRegistry() *cryodecoder.CodecRegistry {
	registry := cryodecoder.NewCodecRegistry()

	// Register all built-in primitive types.
	registry.RegisterPrimitives()

	// Register all custom structs the server expects to receive.
	_, err := registry.RegisterStruct(GameUpdate{})
	if err != nil {
		log.Fatalf("Failed to register GameUpdate: %v", err)
	}
	_, err = registry.RegisterStruct(PlayerLogin{})
	if err != nil {
		log.Fatalf("Failed to register PlayerLogin: %v", err)
	}
	_, err = registry.RegisterStruct(ChatMessage{})
	if err != nil {
		log.Fatalf("Failed to register ChatMessage: %v", err)
	}

	return registry
}

func main() {
	registry := setupRegistry()
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
	defer listener.Close()
	fmt.Println("Server started on :8080, waiting for connections...")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %v", err)
			continue
		}
		go handleConnection(conn, registry)
	}
}

// handleConnection processes a single client connection.
func handleConnection(conn net.Conn, registry *cryodecoder.CodecRegistry) {
	defer conn.Close()
	clientAddr := conn.RemoteAddr().String()
	fmt.Printf("Client connected from %s\n", clientAddr)

	decoder := cryodecoder.NewDecoder(registry, conn)
	update, err := decoder.Decode()
	if err != nil {
		log.Printf("Error decoding from %s: %v", clientAddr, err)
		return
	}

	// Use a type switch to handle the different possible message types.
	fmt.Printf("\n--- Received Message from %s ---\n", clientAddr)
	switch v := update.(type) {
	case GameUpdate:
		fmt.Printf("Type: GameUpdate\n")
		fmt.Printf("  Player ID:   %d\n", v.PlayerID)
		fmt.Printf("  Player Name: %s\n", v.PlayerName)
		fmt.Printf("  Score:       %.2f\n", v.Score)
		fmt.Printf("  Stats:       Kills: %d, Deaths: %d, Accuracy: %.2f\n",
			v.Stats.Kills, v.Stats.Deaths, v.Stats.Accuracy)

	case PlayerLogin:
		fmt.Printf("Type: PlayerLogin\n")
		fmt.Printf("  Player: %s\n", v.PlayerName)
		fmt.Printf("  Login Time: %d\n", v.Timestamp)

	case ChatMessage:
		fmt.Printf("Type: ChatMessage\n")
		fmt.Printf("  Sender ID: %d\n", v.SenderID)
		fmt.Printf("  Message:   %s\n", v.MessageText)
		fmt.Printf("  Global:    %t\n", v.IsGlobal)

	default:
		// Fallback for unknown types
		log.Printf("Received unexpected data type from %s: %T", clientAddr, v)
		return
	}
	fmt.Println("----------------------------------------")
}

```
