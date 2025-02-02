/*
Copyright 2020 Elotl Inc

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This package was originally designed to have 3 channels for reading
// that represented stdin, stdout, stderr, also a channel that
// contained the exit code.  Additional channels can be created by the
// user.
package wsstream

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var (
	wsBufSize = 0
)

const (
	StdinChan    int = 0
	StdoutChan   int = 1
	StderrChan   int = 2
	ExitCodeChan int = 3
)

type MessageFrame []byte

type WebsocketParams struct {
	// Time allowed to write the file to the client.
	writeWait time.Duration
	// Time we're allowed to wait for a pong reply
	pongWait time.Duration
	// Send pings to client with this period. Must be less than pongWait.
	pingPeriod time.Duration
}

type WSStream struct {
	// The reader will close(closed), that's how we detect the
	// connection has been shut down
	closed chan struct{}
	// If we want to close this from the server side, we fire
	// a message into closeMsgChan, that'll write a close
	// message from the write loop
	closeMsgChan chan struct{}
	// writeChan is used internally to pump messages to the write
	// loop, this ensures we only write from one goroutine (writing is
	// not threadsafe).
	writeChan chan []byte
	// We write the received messages to readRawChan,
	// standard readChans are nil.
	readChan chan []byte
	// Websocket parameters
	params WebsocketParams
	// The underlying gorilla websocket object
	conn *websocket.Conn
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	ws := &WSStream{
		readChan:     make(chan []byte, wsBufSize),
		closed:       make(chan struct{}),
		writeChan:    make(chan []byte, wsBufSize),
		closeMsgChan: make(chan struct{}),
		params: WebsocketParams{
			writeWait:  10 * time.Second,
			pongWait:   15 * time.Second,
			pingPeriod: 10 * time.Second,
		},
		conn: conn,
	}
	go ws.StartReader()
	go ws.StartWriteLoop()
	return ws
}

func (ws *WSStream) Closed() <-chan struct{} {
	return ws.closed
}

// CloseAndCleanup MUST be called.  Should be called by the user of
// the stream in response to hearing about the close from selecting on
// Closed().  Should only be called once but MUST be called by the
// user of the WSStream or else we'll leak the open WS connection.
func (ws *WSStream) CloseAndCleanup() error {
	select {
	case <-ws.closed:
		// if we've already closed the conn then dont' try to write on
		// the conn.
		return io.EOF
	default:
		// If we haven't already closed the connection (ws.closed),
		// then write a closed message, wait for it to be sent and
		// then close the underlying connection
		ws.closeMsgChan <- struct{}{}
		<-ws.closeMsgChan
	}

	// It's possible we want to wrap this in a sync.Once but for now,
	// all clients are pretty clean since they just create the
	// websocket and defer ws.Close()
	return ws.conn.Close()
}

func (ws *WSStream) ReadMsg() <-chan []byte {
	return ws.readChan
}

func (ws *WSStream) WriteMsg(channel int, msg []byte) error {
	return ws.write(channel, msg)
}

func (ws *WSStream) WriteRaw(framedMsg []byte) error {
	select {
	case <-ws.closed:
		return io.EOF
	default:
		ws.writeChan <- framedMsg
		return nil
	}
}

func UnpackMessage(frame []byte) (int, []byte, error) {
	if len(frame) == 0 {
		return 0, []byte(""), nil
	}
	channel := frame[0] - '0'
	msg, err := base64.StdEncoding.DecodeString(string(frame[1:]))
	return int(channel), msg, err
}

func PackMessage(channel int, data []byte) []byte {
	frame := string('0'+channel) + base64.StdEncoding.EncodeToString(data)
	return []byte(frame)
}

func (ws *WSStream) write(channel int, msg []byte) error {
	select {
	case <-ws.closed:
		return io.EOF
	default:
		f := PackMessage(channel, msg)
		ws.writeChan <- f
	}
	return nil
}

func (ws *WSStream) StartReader() {
	ws.conn.SetReadDeadline(time.Now().Add(ws.params.pongWait))
	ws.conn.SetPongHandler(func(string) error {
		ws.conn.SetReadDeadline(time.Now().Add(ws.params.pongWait))
		return nil
	})
	for {
		_, msg, err := ws.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) &&
				!strings.Contains(err.Error(), "closed network connection") {
				log.Println("Closing connection after error:", err)
				fmt.Printf("%#v\n", err)
			}
			close(ws.closed)
			return
		}
		ws.readChan <- msg
	}
}

func (ws *WSStream) StartWriteLoop() {
	pingTicker := time.NewTicker(ws.params.pingPeriod)
	defer pingTicker.Stop()
	for {
		select {
		case <-ws.closed:
			return
		case msg := <-ws.writeChan:
			_ = ws.conn.SetWriteDeadline(time.Now().Add(ws.params.writeWait))
			err := ws.conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Println("Error writing msg:", err)
			}
		case <-ws.closeMsgChan:
			_ = ws.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			ws.closeMsgChan <- struct{}{}
		case <-pingTicker.C:
			_ = ws.conn.SetWriteDeadline(time.Now().Add(ws.params.writeWait))
			err := ws.conn.WriteMessage(websocket.PingMessage, []byte{})
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					log.Println("Abnormal error in ping loop:", err)
				}
				return
			}
		}
	}
}
