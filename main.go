package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/huin/goupnp/dcps/av1"
)

// Audio streaming constants
const (
	SampleRate      = 44100
	Channels        = 2
	BitsPerSample   = 16
	BytesPerSecond  = SampleRate * Channels * (BitsPerSample / 8) // 176,400 bytes/second
	ChunkSize       = 4096
	DefaultPort     = ":8080"
	HttpTimeout     = 10 * time.Second
)

// DIDL-Lite XML template for UPnP metadata
const didlTemplate = `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" xmlns:r="urn:schemas-rinconnetworks-com:metadata-1-0/" xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/">
<item id="-1" parentID="-1" restricted="true">
<res protocolInfo="http-get:*:audio/wav:*" duration="0:00:00">%s</res>
<r:streamContent></r:streamContent>
<r:radioShowMd></r:radioShowMd>
<upnp:class>object.item.audioItem.audioBroadcast</upnp:class>
<dc:title>%s</dc:title>
</item>
</DIDL-Lite>`


// Stream starts streaming audio from the provided reader to the specified device
func Stream(ctx context.Context, device *av1.AVTransport1, reader io.Reader, title, localAddr, port string) error {
	if port == "" {
		port = DefaultPort
	}

	// Start HTTP server immediately to accept connections
	listener, err := net.Listen("tcp", port)
	if err != nil {
		return fmt.Errorf("failed to create listener: %v", err)
	}

	httpServer := &http.Server{}
	mux := http.NewServeMux()

	// Channel to signal when client disconnects
	clientDone := make(chan struct{}, 1)

	// Create handler as closure that captures the reader
	// NOTE: This handler and its client disconnect logic is tested in
	// TestStreamClientDisconnectWithMockUPnP using a mock UPnP server.
	mux.HandleFunc("/audio.wav", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Client connected: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		defer func() {
			log.Printf("Client disconnected: %s", r.RemoteAddr)
			select {
			case clientDone <- struct{}{}:
			default:
			}
		}()
		handleAudioStream(w, r, reader)
	})
	httpServer.Handler = mux

	// Start server in goroutine
	serverDone := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverDone <- err
		} else {
			serverDone <- nil
		}
	}()

	// Ensure server shutdown when context is cancelled
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	// Server is immediately ready since listener is already bound

	// Get local IP address
	var localIP string
	if localAddr != "" {
		localIP = localAddr
	} else {
		var err error
		localIP, err = getLocalIP()
		if err != nil {
			return fmt.Errorf("failed to get local IP: %v (try specifying -addr flag)", err)
		}
	}

	// Build audio URL with local IP
	audioURL := fmt.Sprintf("http://%s%s/audio.wav", localIP, port)

	// Set the audio URL on the UPnP device with metadata
	metadata := fmt.Sprintf(didlTemplate, audioURL, title)
	if err := device.SetAVTransportURICtx(ctx, 0, audioURL, metadata); err != nil {
		return fmt.Errorf("failed to set AV transport URI: %v", err)
	}


	// Start playback
	if err := device.PlayCtx(ctx, 0, "1"); err != nil {
		return fmt.Errorf("playback failed: %v", err)
	}


	fmt.Println("Streaming audio... Press Ctrl+C to stop")

	// Wait for context cancellation, client disconnect, or server error
	// NOTE: The clientDone case below is tested in TestStreamClientDisconnectWithMockUPnP.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-clientDone:
		fmt.Println("Client disconnected, stopping stream")
		return nil
	case err := <-serverDone:
		if err != nil {
			return fmt.Errorf("HTTP server error: %v", err)
		}
		return nil
	}
}

var verbose = flag.Bool("v", false, "Enable verbose logging")

func main() {
	flag.Usage = printUsage
	flag.Parse()

	// Disable logging if not verbose
	if !*verbose {
		log.SetOutput(io.Discard)
	}

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

	args := flag.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "discover":
		err = discoverCommand()
	case "play":
		err = playCommand(ctx)
	default:
		err = fmt.Errorf("Unknown command: %s", args[0])
		printUsage()
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("Streaming stopped")
			os.Exit(0)
		} else {
			fmt.Fprintf(os.Stderr, "Command failed: %v\n", err)
			os.Exit(1)
		}
	}
}


func printUsage() {
	fmt.Println("Usage: airplayer [options] <command> [command-options]")
	fmt.Println()
	fmt.Println("Airplayer streams PCM or WAV audio from stdin to UPnP/DLNA speakers (like Sonos)")
	fmt.Println("over the network. It's suitable for use with MPD's Pipe output.")
	fmt.Println()
	fmt.Println("Global options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  discover        List all UPnP AV devices on the network")
	fmt.Println("  play            Stream PCM audio from stdin to a UPnP speaker")
	fmt.Println()
	fmt.Println("Use 'airplayer <command> -h' for command-specific help")
}

func discoverCommand() error {
	discoverFlags := flag.NewFlagSet("discover", flag.ExitOnError)

	discoverFlags.Usage = func() {
		fmt.Println("Usage: airplayer discover")
		fmt.Println()
		fmt.Println("List all UPnP AV devices on the network")
	}

	// Skip the command name itself
	args := flag.Args()[1:]
	discoverFlags.Parse(args)

	clients, _, err := av1.NewAVTransport1Clients()
	if err != nil {
		return fmt.Errorf("error discovering devices: %v", err)
	}
	devices := clients

	if len(devices) == 0 {
		fmt.Println("No UPnP AV Transport devices found")
		return nil
	}

	fmt.Printf("Found %d UPnP device(s):\n", len(devices))
	for i, device := range devices {
		deviceURL := device.ServiceClient.RootDevice.URLBase.Host
		deviceIP := getDeviceIP(deviceURL)
		deviceInfo := device.ServiceClient.RootDevice.Device

		// Try to get room name from device description XML
		roomName := getRoomName(device.ServiceClient.RootDevice.URLBase.String())

		fmt.Printf("  %d. %s\n", i+1, deviceInfo.FriendlyName)
		if roomName != "" {
			fmt.Printf("     Room: %s\n", roomName)
		}
		fmt.Printf("     IP: %s\n", deviceIP)
		if deviceInfo.ModelName != "" {
			fmt.Printf("     Model: %s\n", deviceInfo.ModelName)
		}
		if deviceInfo.ModelDescription != "" {
			fmt.Printf("     Description: %s\n", deviceInfo.ModelDescription)
		}
		fmt.Printf("     URL: %s\n", device.ServiceClient.RootDevice.URLBase.String())
		fmt.Println()
	}
	return nil
}

type SonosDevice struct {
	XMLName xml.Name        `xml:"root"`
	Device  SonosDeviceInfo `xml:"device"`
}

type SonosDeviceInfo struct {
	RoomName     string `xml:"roomName"`
	DisplayName  string `xml:"displayName"`
	FriendlyName string `xml:"friendlyName"`
}

func getRoomName(deviceURL string) string {
	client := &http.Client{Timeout: HttpTimeout}
	resp, err := client.Get(deviceURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to fetch device XML from %s: %v\n", deviceURL, err)
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read device XML from %s: %v\n", deviceURL, err)
		return ""
	}

	return parseRoomNameFromXML(body, deviceURL)
}

func parseRoomNameFromXML(xmlData []byte, deviceURL string) string {
	var sonosDevice SonosDevice
	if err := xml.Unmarshal(xmlData, &sonosDevice); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse device XML from %s: %v\n", deviceURL, err)
		return ""
	}

	if sonosDevice.Device.RoomName != "" {
		return sonosDevice.Device.RoomName
	}

	if sonosDevice.Device.DisplayName != "" {
		return sonosDevice.Device.DisplayName
	}

	return ""
}

func getLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %v", err)
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			// Return first non-loopback IPv4 address
			if ip != nil && !ip.IsLoopback() && ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable network interface found")
}


// discoverAndFindDevice performs UPnP discovery and finds a device by room name/IP (or returns first if query is empty)
func discoverAndFindDevice(query string) (*av1.AVTransport1, error) {
	clients, _, err := av1.NewAVTransport1Clients()
	if err != nil {
		return nil, fmt.Errorf("error discovering devices: %v", err)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("no UPnP AV Transport devices found")
	}

	log.Printf("Discovered %d UPnP device(s)", len(clients))

	// If no query, return first device
	if query == "" {
		return clients[0], nil
	}

	// Search for device by room name or IP (IP search is for fallback from direct connection)
	for _, device := range clients {
		deviceURL := device.ServiceClient.RootDevice.URLBase.Host
		deviceIP := getDeviceIP(deviceURL)
		roomName := getRoomName(device.ServiceClient.RootDevice.URLBase.String())

		// For -name parameter, only match room names
		// For IP fallback, match IP addresses
		if roomName == query || deviceIP == query {
			return device, nil
		}
	}

	return nil, fmt.Errorf("device '%s' not found", query)
}

func playCommand(ctx context.Context) error {
	playFlags := flag.NewFlagSet("play", flag.ExitOnError)
	deviceURL := playFlags.String("deviceURL", "", "UPnP device description URL (e.g., http://192.168.1.233:1400/xml/device_description.xml)")
	deviceName := playFlags.String("deviceName", "", "UPnP device room name (e.g., \"Living Room\")")
	streamTitle := playFlags.String("title", "Airplayer Stream", "Title to display for the audio stream")
	localAddr := playFlags.String("addr", "", "Local IP address to bind to (optional, will auto-detect if empty)")
	port := playFlags.String("port", DefaultPort, "Port to bind HTTP server to (default: :8080)")

	playFlags.Usage = func() {
		fmt.Println("Usage: airplayer play [options]")
		fmt.Println()
		fmt.Println("Stream PCM audio from stdin to a UPnP speaker")
		fmt.Println()
		fmt.Println("Options:")
		playFlags.PrintDefaults()
		fmt.Println()
		fmt.Println("Note: Use either -deviceURL OR -deviceName, not both. If neither is specified, uses first discovered device.")
	}

	// Skip the command name itself
	args := flag.Args()[1:]
	playFlags.Parse(args)

	// Validate that both -deviceURL and -deviceName are not specified
	if *deviceURL != "" && *deviceName != "" {
		return fmt.Errorf("cannot specify both -deviceURL and -deviceName flags")
	}

	var device *av1.AVTransport1
	var err error

	if *deviceURL != "" {
		// Connect directly using the provided device description URL
		u, err := url.Parse(*deviceURL)
		if err != nil {
			return fmt.Errorf("invalid device URL: %v", err)
		}

		clients, err := av1.NewAVTransport1ClientsByURL(u)
		if err != nil {
			return fmt.Errorf("failed to connect to device at %s: %v", *deviceURL, err)
		}

		if len(clients) == 0 {
			return fmt.Errorf("no AVTransport service found at %s", *deviceURL)
		}

		device = clients[0]
		fmt.Printf("Connected directly to device at %s\n", *deviceURL)
	} else {
		// Use discovery (either for name search or first device)
		device, err = discoverAndFindDevice(*deviceName)
		if err != nil {
			return err
		}
	}

	return Stream(ctx, device, os.Stdin, *streamTitle, *localAddr, *port)
}

func getDeviceIP(deviceURL string) string {
	// Handle IPv6 addresses enclosed in brackets
	if strings.Contains(deviceURL, "[") {
		if !strings.Contains(deviceURL, "]") {
			// Malformed IPv6 (opening bracket but no closing bracket)
			return ""
		}
		start := strings.Index(deviceURL, "[")
		end := strings.Index(deviceURL, "]")
		if start < end && start >= 0 && end > start {
			return deviceURL[start+1 : end]
		}
		// Malformed IPv6, return empty
		return ""
	}

	// Handle IPv4 addresses
	parts := strings.Split(deviceURL, ":")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func handleAudioStream(w http.ResponseWriter, r *http.Request, audioReader io.Reader) {
	// Read first 4 bytes to detect format when request comes in
	log.Printf("Starting audio stream processing")

	header := make([]byte, 4)
	n, err := io.ReadFull(audioReader, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		log.Printf("Failed to read format header: %v", err)
		http.Error(w, "Failed to read audio data", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		log.Printf("No audio data received from stdin")
		http.Error(w, "No audio data", http.StatusNoContent)
		return
	}

	// Check if data is already WAV format or raw PCM
	isWAV := n >= 4 && string(header[:4]) == "RIFF"
	log.Printf("Detected audio format: %s (header bytes: %d)", map[bool]string{true: "WAV", false: "PCM"}[isWAV], n)

	// Set headers for audio streaming
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-cache")

	audioStream := io.MultiReader(bytes.NewReader(header[:n]), audioReader)

	if isWAV {
		log.Printf("Streaming WAV data directly with throttling")
		bytesWritten, err := throttledCopy(w, audioStream, ChunkSize)
		if err != nil {
			log.Printf("WAV streaming error after %d bytes: %v", bytesWritten, err)
		} else {
			log.Printf("WAV streaming completed: %d bytes total", bytesWritten)
		}
	} else {
		log.Printf("Converting PCM to WAV and streaming with throttling")
		// Add WAV header for PCM data with large size estimate
		writeWAVHeader(w, 0xFFFFFFFF-44) // Maximum size to avoid early disconnection
		bytesWritten, err := throttledCopy(w, audioStream, ChunkSize)
		if err != nil {
			log.Printf("PCM streaming error after %d bytes: %v", bytesWritten, err)
		} else {
			log.Printf("PCM streaming completed: %d bytes total", bytesWritten)
		}
	}
}

func writeWAVHeader(w io.Writer, dataSize uint32) {
	// WAV header for 16-bit stereo PCM at 44.1kHz
	header := []byte{
		// RIFF header
		'R', 'I', 'F', 'F',
		0, 0, 0, 0, // File size (will be filled later if known)
		'W', 'A', 'V', 'E',

		// fmt chunk
		'f', 'm', 't', ' ',
		16, 0, 0, 0, // fmt chunk size
		1, 0, // Audio format (PCM)
		2, 0, // Number of channels (stereo)
		0x44, 0xAC, 0, 0, // Sample rate (44100)
		0x10, 0xB1, 2, 0, // Byte rate (44100 * 2 * 2)
		4, 0, // Block align (2 * 2)
		16, 0, // Bits per sample

		// data chunk
		'd', 'a', 't', 'a',
		0, 0, 0, 0, // Data size (unknown for streaming)
	}

	// Set file size if known
	if dataSize > 0 {
		fileSize := dataSize + 36
		header[4] = byte(fileSize)
		header[5] = byte(fileSize >> 8)
		header[6] = byte(fileSize >> 16)
		header[7] = byte(fileSize >> 24)

		header[40] = byte(dataSize)
		header[41] = byte(dataSize >> 8)
		header[42] = byte(dataSize >> 16)
		header[43] = byte(dataSize >> 24)
	}

	w.Write(header)
}

func throttledCopy(dst io.Writer, src io.Reader, chunkSize int) (int64, error) {
	var written int64
	var chunkCount int64
	buf := make([]byte, chunkSize)
	startTime := time.Now()
	lastReportTime := startTime
	firstReportDone := false

	log.Printf("Starting throttled copy with chunk size: %d bytes", chunkSize)

	for {
		n, err := src.Read(buf)

		if n > 0 {
			chunkCount++

			nw, ew := dst.Write(buf[:n])

			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				log.Printf("Write error after %d chunks, %d bytes: %v", chunkCount, written, ew)
				return written, ew
			}
			if n != nw {
				log.Printf("Short write: read %d bytes, wrote %d bytes", n, nw)
				return written, io.ErrShortWrite
			}

			// Report streaming progress: first report after 5s, then every 20s
			now := time.Now()
			var shouldReport bool
			if !firstReportDone && now.Sub(startTime) >= 5*time.Second {
				shouldReport = true
				firstReportDone = true
			} else if firstReportDone && now.Sub(lastReportTime) >= 20*time.Second {
				shouldReport = true
			}

			if shouldReport {
				elapsedTotal := now.Sub(startTime)
				throughputBytesPerSec := float64(written) / elapsedTotal.Seconds()
				throughputKbps := throughputBytesPerSec * 8 / 1000
				log.Printf("Streaming: %.1f MB in %v, %.1f KB/s (%.1f%% of realtime)",
					float64(written)/(1024*1024), elapsedTotal.Round(time.Second), throughputKbps, (throughputBytesPerSec/float64(BytesPerSecond))*100)
				lastReportTime = now
			}
		}
		if err == io.EOF {
			log.Printf("Reached EOF after %.1f MB total", float64(written)/(1024*1024))
			break
		}
		if err != nil {
			log.Printf("Read error after %.1f MB: %v", float64(written)/(1024*1024), err)
			return written, err
		}

		// Calculate how long we should have taken to stream this much data
		expectedTime := time.Duration(float64(written) / float64(BytesPerSecond) * 1e9) // nanoseconds
		elapsedTime := time.Since(startTime)

		// If we're going too fast, sleep to throttle to real-time audio playback
		if elapsedTime < expectedTime {
			sleepTime := expectedTime - elapsedTime
			time.Sleep(sleepTime)
		}
	}

	// Final report
	totalDuration := time.Since(startTime)
	avgThroughput := float64(written) / totalDuration.Seconds()
	log.Printf("Streaming completed: %.1f MB in %v (avg: %.1f KB/s)",
		float64(written)/(1024*1024), totalDuration, avgThroughput*8/1000)

	return written, nil
}
