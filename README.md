# airplayer

Airplayer streams PCM or WAV audio from stdin to UPnP/DLNA speakers (like Sonos)
over the network. It's suitable for use with [MPD](https://www.musicpd.org)'s
[Pipe output](https://mpd.readthedocs.io/en/latest/plugins.html#pipe).

## Usage

```bash
# Discover UPnP devices
> airplayer discover
Found 5 UPnP device(s):
  1. 192.168.1.215 - Sonos One SL - RINCON_38420B5BD20C01400
     Room: Bedroom
     IP: 192.168.1.215
     Model: Sonos One SL
     Description: Sonos One SL
     URL: http://192.168.1.215:1400/xml/device_description.xml

[...]
```

```bash
# Stream from stdin to first available device
> cat taxman.wav | airplayer play
Streaming audio... Press Ctrl+C to stop
^CStreaming stopped
```

```bash
# Stream to specific device by room name
∇ /u/h/a/airplayer ⋊> cat taxman.wav | airplayer play -deviceName Office
Streaming audio... Press Ctrl+C to stop
^CStreaming stopped
```

```bash
# Stream to specific device by URL
> cat taxman.wav | airplayer play -deviceURL 'http://192.168.1.233:1400/xml/device_description.xml'
Connected directly to device at http://192.168.1.233:1400/xml/device_description.xml
Streaming audio... Press Ctrl+C to stop
^CStreaming stopped
```

## Options

```bash
> airplayer -h
Usage: airplayer [options] <command> [command-options]

Airplayer streams PCM or WAV audio from stdin to UPnP/DLNA speakers (like Sonos)
over the network. It's suitable for use with MPD's Pipe output.

Global options:
  -v    Enable verbose logging

Commands:
  discover        List all UPnP AV devices on the network
  play            Stream PCM audio from stdin to a UPnP speaker

Use 'airplayer <command> -h' for command-specific help
```

```bash
> airplayer play -h
Usage: airplayer play [options]

Stream PCM audio from stdin to a UPnP speaker

Options:
  -addr string
        Local IP address to bind to (optional, will auto-detect if empty)
  -deviceName string
        UPnP device room name (e.g., "Living Room")
  -deviceURL string
        UPnP device description URL (e.g., http://192.168.1.233:1400/xml/device_description.xml)
  -port string
        Port to bind HTTP server to (default: :8080) (default ":8080")
  -title string
        Title to display for the audio stream (default "Airplayer Stream")

Note: Use either -deviceURL OR -deviceName, not both. If neither is specified, uses first discovered device.
```

```bash
> airplayer discover -h
Usage: airplayer discover

List all UPnP AV devices on the network
```
