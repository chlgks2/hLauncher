// Package pipe implements the named-pipe protocol that the injected kDetector
// speaks to the launcher, reconstructed from the original kLauncher by static
// analysis.
//
// Wire format (per message):
//
//	[uint32 little-endian: payload length][payload]
//
// payload is JSON: {"command":"<name>","data":{...}}
//
// When kDetector connects to \\.\pipe\KLauncherPipe, the launcher must
// immediately send two messages, or kDetector shuts the game down:
//
//	{"command":"set_detector_path","data":{"launcherPath","appDataPath","version"}}
//	{"command":"send_token","data":{"userToken","userNickName"}}
package pipe

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/Microsoft/go-winio"
)

// Pipe names (server side; the launcher is the server).
const (
	KLauncherPipeName = `\\.\pipe\KLauncherPipe`
	BattleNetPipeName = `\\.\pipe\BattleNetPipe`
)

// maxPayload matches the original's 100 MiB frame cap.
const maxPayload = 0x6400000

// Message is one JSON pipe message.
type Message struct {
	Command string                 `json:"command"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// HandshakeInfo holds the values sent to kDetector on connect.
type HandshakeInfo struct {
	LauncherPath string
	AppDataPath  string
	Version      string
	UserToken    string
	UserNickName string
}

// Logf is a simple logging callback.
type Logf func(format string, args ...interface{})

// KLauncherPipe is the launcher-side server for kDetector.
type KLauncherPipe struct {
	name     string
	info     HandshakeInfo
	logf     Logf
	listener net.Listener

	mu        sync.Mutex
	onMessage func(Message)
	closed    bool
}

// NewKLauncherPipe creates the pipe server (not yet listening).
func NewKLauncherPipe(info HandshakeInfo, logf Logf) *KLauncherPipe {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &KLauncherPipe{name: KLauncherPipeName, info: info, logf: logf}
}

// SetInfo updates the handshake values (e.g. when config/paths change).
func (p *KLauncherPipe) SetInfo(info HandshakeInfo) {
	p.mu.Lock()
	p.info = info
	p.mu.Unlock()
}

// OnMessage registers a callback for messages received from kDetector.
func (p *KLauncherPipe) OnMessage(fn func(Message)) {
	p.mu.Lock()
	p.onMessage = fn
	p.mu.Unlock()
}

// Listen starts the pipe server and accepts connections in the background.
func (p *KLauncherPipe) Listen() error {
	l, err := winio.ListenPipe(p.name, &winio.PipeConfig{
		// The original used a message-mode pipe (win32MessageBytePipe).
		MessageMode:      true,
		InputBufferSize:  65536,
		OutputBufferSize: 65536,
	})
	if err != nil {
		return fmt.Errorf("pipe: ListenPipe(%s): %w", p.name, err)
	}
	p.listener = l
	p.logf("KLauncherPipe listening on %s", p.name)
	go p.acceptLoop()
	return nil
}

// Close stops the server.
func (p *KLauncherPipe) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	if p.listener != nil {
		return p.listener.Close()
	}
	return nil
}

func (p *KLauncherPipe) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *KLauncherPipe) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.isClosed() {
				return
			}
			p.logf("KLauncherPipe accept error: %v", err)
			return
		}
		p.logf("KLauncherPipe: kDetector connected")
		go p.handleConn(conn)
	}
}

func (p *KLauncherPipe) handleConn(conn net.Conn) {
	defer conn.Close()

	// Send the handshake the moment the client connects.
	if err := p.sendHandshake(conn); err != nil {
		p.logf("KLauncherPipe: handshake send failed: %v", err)
		return
	}
	p.readLoop(conn)
	p.logf("KLauncherPipe: kDetector disconnected")
}

// sendHandshake sends set_detector_path then send_token.
func (p *KLauncherPipe) sendHandshake(conn net.Conn) error {
	p.mu.Lock()
	info := p.info
	p.mu.Unlock()

	if err := p.Send(conn, Message{
		Command: "set_detector_path",
		Data: map[string]interface{}{
			"launcherPath": info.LauncherPath,
			"appDataPath":  info.AppDataPath,
			"version":      info.Version,
		},
	}); err != nil {
		return err
	}
	return p.Send(conn, Message{
		Command: "send_token",
		Data: map[string]interface{}{
			"userToken":    info.UserToken,
			"userNickName": info.UserNickName,
		},
	})
}

// Send marshals msg to JSON and writes a length-prefixed frame.
func (p *KLauncherPipe) Send(conn net.Conn, msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	_, err = conn.Write(frame)
	return err
}

// readLoop accumulates bytes and extracts length-prefixed frames, mirroring the
// original MessageFrame.ProcessIncomingData accumulation logic.
func (p *KLauncherPipe) readLoop(conn net.Conn) {
	var buf []byte
	tmp := make([]byte, 8192)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for len(buf) >= 4 {
				plen := binary.LittleEndian.Uint32(buf[:4])
				if plen > maxPayload {
					p.logf("KLauncherPipe: frame too large (%d), dropping conn", plen)
					return
				}
				if uint32(len(buf)-4) < plen {
					break // wait for the rest of the frame
				}
				payload := make([]byte, plen)
				copy(payload, buf[4:4+plen])
				buf = buf[4+plen:]
				p.dispatch(payload)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *KLauncherPipe) dispatch(payload []byte) {
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		p.logf("KLauncherPipe: bad JSON from kDetector: %v", err)
		return
	}
	p.mu.Lock()
	fn := p.onMessage
	p.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}
