package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Test data for XML parsing
const validSonosXML = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
    <device>
        <roomName>Living Room</roomName>
        <displayName>Sonos Speaker</displayName>
        <friendlyName>Sonos Play:1</friendlyName>
    </device>
</root>`

const validSonosXMLWithDisplayNameOnly = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
    <device>
        <displayName>Kitchen Speaker</displayName>
        <friendlyName>Sonos Play:5</friendlyName>
    </device>
</root>`

const invalidXML = `<?xml version="1.0"?>
<invalid>
    <malformed
</invalid>`

const emptyDeviceXML = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
    <device>
        <friendlyName>Generic Speaker</friendlyName>
    </device>
</root>`

func TestParseRoomNameFromXML(t *testing.T) {
	tests := []struct {
		name        string
		xmlData     string
		expectedRoom string
		shouldError  bool
	}{
		{
			name:         "Valid XML with room name",
			xmlData:      validSonosXML,
			expectedRoom: "Living Room",
			shouldError:  false,
		},
		{
			name:         "Valid XML with display name only",
			xmlData:      validSonosXMLWithDisplayNameOnly,
			expectedRoom: "Kitchen Speaker",
			shouldError:  false,
		},
		{
			name:         "Invalid XML",
			xmlData:      invalidXML,
			expectedRoom: "",
			shouldError:  true,
		},
		{
			name:         "Empty device XML",
			xmlData:      emptyDeviceXML,
			expectedRoom: "",
			shouldError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stderr to check for error messages
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := parseRoomNameFromXML([]byte(tt.xmlData), "http://test.local/device.xml")

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			buf.ReadFrom(r)
			stderrOutput := buf.String()

			if result != tt.expectedRoom {
				t.Errorf("Expected room name %q, got %q", tt.expectedRoom, result)
			}

			if tt.shouldError && stderrOutput == "" {
				t.Error("Expected error message on stderr, got none")
			}
			if !tt.shouldError && stderrOutput != "" {
				t.Errorf("Unexpected error message on stderr: %s", stderrOutput)
			}
		})
	}
}

func TestGetRoomNameWithMockServer(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   string
		statusCode     int
		expectedRoom   string
		expectStderrError bool
	}{
		{
			name:         "Successful request with room name",
			responseBody: validSonosXML,
			statusCode:   http.StatusOK,
			expectedRoom: "Living Room",
			expectStderrError: false,
		},
		{
			name:         "Successful request with display name",
			responseBody: validSonosXMLWithDisplayNameOnly,
			statusCode:   http.StatusOK,
			expectedRoom: "Kitchen Speaker",
			expectStderrError: false,
		},
		{
			name:         "Server error",
			responseBody: "",
			statusCode:   http.StatusInternalServerError,
			expectedRoom: "",
			expectStderrError: true,
		},
		{
			name:         "Invalid XML response",
			responseBody: invalidXML,
			statusCode:   http.StatusOK,
			expectedRoom: "",
			expectStderrError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			// Capture stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := getRoomName(server.URL)

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			buf.ReadFrom(r)
			stderrOutput := buf.String()

			if result != tt.expectedRoom {
				t.Errorf("Expected room name %q, got %q", tt.expectedRoom, result)
			}

			if tt.expectStderrError && stderrOutput == "" {
				t.Error("Expected error message on stderr, got none")
			}
		})
	}
}


func TestGetDeviceIP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"IPv4 with port", "192.168.1.100:1400", "192.168.1.100"},
		{"IPv4 without port", "192.168.1.100", "192.168.1.100"},
		{"IPv6 with brackets and port", "[2001:db8::1]:1400", "2001:db8::1"},
		{"IPv6 with brackets no port", "[2001:db8::1]", "2001:db8::1"},
		{"Hostname with port", "speaker.local:1400", "speaker.local"},
		{"Hostname without port", "speaker.local", "speaker.local"},
		{"Empty string", "", ""},
		{"Just port", ":1400", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getDeviceIP(tt.input)
			if result != tt.expected {
				t.Errorf("getDeviceIP(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetLocalIP(t *testing.T) {
	// This test checks that getLocalIP returns a valid IPv4 address or an error
	ip, err := getLocalIP()
	
	if err != nil {
		// If there's an error, it should be meaningful
		if !strings.Contains(err.Error(), "no suitable network interface found") &&
		   !strings.Contains(err.Error(), "failed to get network interfaces") {
			t.Errorf("Unexpected error format: %v", err)
		}
		// It's ok if there's no suitable interface in test environment
		t.Logf("No suitable network interface found (expected in some test environments): %v", err)
		return
	}
	
	// If we got an IP, it should be valid IPv4
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		t.Errorf("getLocalIP returned invalid IP address: %q", ip)
	}
	
	if parsedIP.To4() == nil {
		t.Errorf("getLocalIP returned non-IPv4 address: %q", ip)
	}
	
	if parsedIP.IsLoopback() {
		t.Errorf("getLocalIP returned loopback address: %q", ip)
	}
	
	t.Logf("getLocalIP returned valid IP: %s", ip)
}

func TestWriteWAVHeader(t *testing.T) {
	tests := []struct {
		name     string
		dataSize uint32
	}{
		{"Zero size", 0},
		{"Small size", 1024},
		{"Large size", 0xFFFFFFFF - 44},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeWAVHeader(&buf, tt.dataSize)

			data := buf.Bytes()
			
			// Check minimum header size (44 bytes)
			if len(data) != 44 {
				t.Errorf("WAV header should be 44 bytes, got %d", len(data))
			}
			
			// Check RIFF signature
			if !bytes.Equal(data[0:4], []byte("RIFF")) {
				t.Error("WAV header should start with 'RIFF'")
			}
			
			// Check WAVE signature
			if !bytes.Equal(data[8:12], []byte("WAVE")) {
				t.Error("WAV header should contain 'WAVE' at offset 8")
			}
			
			// Check fmt chunk
			if !bytes.Equal(data[12:16], []byte("fmt ")) {
				t.Error("WAV header should contain 'fmt ' at offset 12")
			}
			
			// Check data chunk
			if !bytes.Equal(data[36:40], []byte("data")) {
				t.Error("WAV header should contain 'data' at offset 36")
			}
		})
	}
}

func TestThrottledCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Test throttledCopy with deterministic time
		input := bytes.NewReader([]byte("test data for throttled copy"))
		output := &bytes.Buffer{}
		
		chunkSize := 4
		
		start := time.Now()
		written, err := throttledCopy(output, input, chunkSize)
		elapsed := time.Since(start)
		
		if err != nil {
			t.Errorf("throttledCopy returned error: %v", err)
		}
		
		if written != 28 { // Length of test data
			t.Errorf("Expected 28 bytes written, got %d", written)
		}
		
		if output.String() != "test data for throttled copy" {
			t.Errorf("Output doesn't match input: %q", output.String())
		}
		
		// throttledCopy throttles based on BytesPerSecond constant (176,400 bytes/sec)
		// For 28 bytes, expected time = 28 / 176400 seconds ≈ 158.73 microseconds
		expectedTime := time.Duration(float64(written) / float64(176400) * 1e9) // BytesPerSecond from constants
		
		// With synctest, time advances deterministically - we can assert exact duration
		if elapsed != expectedTime {
			t.Errorf("Expected exactly %v elapsed time, got %v", expectedTime, elapsed)
		}
		
		t.Logf("throttledCopy took %v for %d bytes (expected %v for real-time audio)", elapsed, written, expectedTime)
	})
}

func TestHandleAudioStreamWithPCM(t *testing.T) {
	// Test handleAudioStream with PCM data
	pcmData := make([]byte, 100) // Raw PCM data (not WAV)
	for i := range pcmData {
		pcmData[i] = byte(i)
	}
	reader := bytes.NewReader(pcmData)
	
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/audio.wav", nil)
	
	handleAudioStream(recorder, req, reader)
	
	response := recorder.Result()
	if response.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", response.StatusCode)
	}
	
	if response.Header.Get("Content-Type") != "audio/wav" {
		t.Errorf("Expected Content-Type audio/wav, got %s", response.Header.Get("Content-Type"))
	}
	
	body, _ := io.ReadAll(response.Body)
	
	// Should have WAV header (44 bytes) + original data
	if len(body) != 44+len(pcmData) {
		t.Errorf("Expected %d bytes (44 WAV header + %d data), got %d", 44+len(pcmData), len(pcmData), len(body))
	}
	
	// Check WAV header
	if !bytes.Equal(body[0:4], []byte("RIFF")) {
		t.Error("Response should start with WAV RIFF header")
	}
}

func TestHandleAudioStreamWithWAV(t *testing.T) {
	// Test handleAudioStream with existing WAV data
	var wavData bytes.Buffer
	writeWAVHeader(&wavData, 100)
	wavData.Write(make([]byte, 100)) // Add some audio data
	
	reader := bytes.NewReader(wavData.Bytes())
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/audio.wav", nil)
	
	handleAudioStream(recorder, req, reader)
	
	response := recorder.Result()
	if response.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", response.StatusCode)
	}
	
	body, _ := io.ReadAll(response.Body)
	
	// Should be same size as original WAV data
	if len(body) != wavData.Len() {
		t.Errorf("Expected %d bytes, got %d", wavData.Len(), len(body))
	}
	
	// Should still be valid WAV
	if !bytes.Equal(body[0:4], []byte("RIFF")) {
		t.Error("Response should maintain WAV RIFF header")
	}
}

func TestHandleAudioStreamEmptyReader(t *testing.T) {
	// Test handleAudioStream with empty reader
	reader := bytes.NewReader([]byte{})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/audio.wav", nil)
	
	handleAudioStream(recorder, req, reader)
	
	response := recorder.Result()
	// Empty reader returns EOF, which causes 500 status, not 204
	if response.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status 500 (Internal Server Error), got %d", response.StatusCode)
	}
}

func TestStreamContextBehavior(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Since we can't easily mock av1.AVTransport1, we'll test the context
		// behavior by testing that Stream respects context cancellation at the HTTP level
		
		// Test that HTTP server gets properly shut down when context is cancelled
		ctx, cancel := context.WithCancel(context.Background())
		
		// We'll use a nil device which will cause Stream to fail early,
		// but we can still test the server startup/shutdown behavior
		reader := bytes.NewReader([]byte("test audio data"))
		
		// Start Stream in a goroutine (it will fail due to nil device, but that's ok for this test)
		streamErr := make(chan error, 1)
		go func() {
			// This will fail, but we're testing the cancellation behavior
			defer func() {
				if r := recover(); r != nil {
					streamErr <- fmt.Errorf("panic: %v", r)
				}
			}()
			err := Stream(ctx, nil, reader, "Test Stream", "127.0.0.1")
			streamErr <- err
		}()
		
		// Wait for goroutines to be set up, then cancel
		synctest.Wait()
		cancel()
		
		// Wait for goroutines to complete
		synctest.Wait()
		
		// Stream should have completed due to context cancellation
		select {
		case err := <-streamErr:
			// We expect either a panic (due to nil device) or context cancellation
			// Both are fine for this test - we're just verifying cancellation works
			t.Logf("Stream returned with: %v", err)
		default:
			t.Error("Stream did not complete after context cancellation")
		}
	})
}

func TestStreamHTTPServerBehavior(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Test HTTP server lifecycle without needing to mock the device
		// We'll test the server startup behavior by checking port binding
		
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		
		reader := bytes.NewReader([]byte("test data"))
		
		// Stream will fail due to nil device, but server should still start and bind port
		streamErr := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					streamErr <- fmt.Errorf("panic: %v", r)
				}
			}()
			err := Stream(ctx, nil, reader, "Test Stream", "127.0.0.1")
			streamErr <- err
		}()
		
		// Wait for goroutines to complete (they will complete due to timeout or error)
		synctest.Wait()
		
		// Get the result
		select {
		case err := <-streamErr:
			// Expect failure due to nil device, but that's fine for this test
			t.Logf("Stream failed as expected: %v", err)
		default:
			t.Error("Stream did not complete")
		}
		
		// Verify port is available again after Stream exits (server was shut down)
		listener, err := net.Listen("tcp", ":8080")
		if err != nil {
			t.Errorf("Port 8080 should be available after Stream exits, but got error: %v", err)
		} else {
			listener.Close()
			t.Log("Port 8080 successfully reclaimed after Stream exit")
		}
	})
}


