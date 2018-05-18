package plug

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bytesProtocol = "milpa.bytes"
)

var (
	wsBufSize = 1000
)

type FrameType string

const (
	FrameTypeMessage  FrameType = "Message"
	FrameTypeExitCode FrameType = "ExitCode"
)

type Frame struct {
	Protocol string    `json:"protocol"`
	Type     FrameType `json:"type"`
	Channel  uint32    `json:"channel"`
	Message  []byte    `json:"message"`
	ExitCode uint32    `json:"exitCode"`
}

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
	// readChans are the 3 channels the user can read from.  The idea
	// is that those can be used to carry stdin, stdout and stderr
	// messages.  But it's up to the users of the library for how to
	// interpret the channels.
	readChans map[uint32]chan []byte
	// If an exit code message comes through, it'll be placed into
	// exitCodeChan
	exitCodeChan chan uint32
	// Websocket parameters
	params WebsocketParams
	// The underlying gorilla websocket object
	conn *websocket.Conn
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	ws := &WSStream{
		closed: make(chan struct{}),
		readChans: map[uint32]chan []byte{
			uint32(0): make(chan []byte, wsBufSize),
			uint32(1): make(chan []byte, wsBufSize),
			uint32(2): make(chan []byte, wsBufSize),
		},
		writeChan:    make(chan []byte, wsBufSize),
		exitCodeChan: make(chan uint32, 1),
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

// CloseAndCleanup must be called.  Should be called by the user of
// the stream in response to hearing about the close from selecting on
// Closed().  Should only be called once but MUST be called by the
// user of the WSStream or else we'll leak the open WS connection.
func (ws *WSStream) CloseAndCleanup() error {
	select {
	case <-ws.closed:
		// if we've already closed the conn then dont' try to write on
		// the conn.
	default:
		//
		ws.closeMsgChan <- struct{}{}
		<-ws.closeMsgChan
	}

	// It's possible we want to wrap this in a sync.Once but for now,
	// all clients are pretty clean since they just create the
	// websocket and defer ws.Close()
	return ws.conn.Close()
}

func (ws *WSStream) ReadChan(channel int) <-chan []byte {
	return ws.readChans[uint32(channel)]
}

func (ws *WSStream) ExitCode() <-chan uint32 {
	return ws.exitCodeChan
}

func (ws *WSStream) Write(channel int, msg []byte) error {
	select {
	case <-ws.closed:
		return fmt.Errorf("Cannot write to a closed websocket")
	default:
		f := Frame{
			Protocol: "milpa.bytes",
			Type:     FrameTypeMessage,
			Channel:  uint32(channel),
			Message:  msg,
		}
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		ws.writeChan <- b
	}
	return nil
}

func (ws *WSStream) StartReader() {
	defer fmt.Println("Exiting read loop")
	ws.conn.SetReadDeadline(time.Now().Add(ws.params.pongWait))
	ws.conn.SetPongHandler(func(string) error {
		ws.conn.SetReadDeadline(time.Now().Add(ws.params.pongWait))
		return nil
	})
	for {
		_, msg, err := ws.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				log.Println("cleaning closing connection")
			} else {
				log.Println("Closing connection after error:", err)
			}
			close(ws.closed)
			return
		}
		f := Frame{}
		err = json.Unmarshal(msg, &f)
		if err != nil {
			fmt.Println("Corrupted message", err)
			continue
		}
		if f.Type == FrameTypeMessage {
			c, exists := ws.readChans[f.Channel]
			if !exists {
				fmt.Println(
					"Websocket reader recieved a message on unknown channel",
					f.Channel)
				continue
			}
			c <- f.Message
		} else if f.Type == FrameTypeExitCode {
			fmt.Println("Exit code:", f.ExitCode)
			ws.exitCodeChan <- f.ExitCode
			// Todo, not sure if we should exit here...
		} else {
			fmt.Println("Unknown websocket frame type:", f.Type)
			continue
		}
	}
}

func (ws *WSStream) StartWriteLoop() {
	pingTicker := time.NewTicker(ws.params.pingPeriod)
	defer pingTicker.Stop()
	defer fmt.Println("Exiting write loop")
	for {
		select {
		case <-ws.closed:
			return
		case msg := <-ws.writeChan:
			_ = ws.conn.SetWriteDeadline(time.Now().Add(ws.params.writeWait))
			err := ws.conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				fmt.Println("error writing")
			}
		case <-ws.closeMsgChan:
			fmt.Println("Writing close")
			_ = ws.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			ws.closeMsgChan <- struct{}{}
		case <-pingTicker.C:
			_ = ws.conn.SetWriteDeadline(time.Now().Add(ws.params.writeWait))
			err := ws.conn.WriteMessage(websocket.PingMessage, []byte{})
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					fmt.Println("closing ping loop, successfully")
				} else {
					fmt.Println("error in ping loop, exiting")
				}
				return
			}
		}
	}
}
