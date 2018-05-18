package plug

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

const (
	stdinMsg  = "stdinMsg"
	stdoutMsg = "stdoutMsg"
	stderrMsg = "stderrMsg"
)

func readChanTimeout(c <-chan []byte, t time.Duration) ([]byte, error) {
	select {
	case m := <-c:
		return m, nil
	case <-time.After(t):
		return nil, fmt.Errorf("timeout")
	}
}

func makeWSHandler(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		wsUpgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		}

		ws, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			if _, ok := err.(websocket.HandshakeError); !ok {
				log.Println(err)
			}
			return
		}
		runServer(t, ws)
	}
}

func runServer(t *testing.T, conn *websocket.Conn) {
	ws := NewWSStream(conn)
	defer ws.CloseAndCleanup()
	val, err := readChanTimeout(ws.ReadChan(0), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, stdinMsg, string(val))
	err = ws.Write(1, []byte(stdoutMsg))
	assert.NoError(t, err)
	err = ws.Write(2, []byte(stderrMsg))
	assert.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
}

func runClient(t *testing.T, conn *websocket.Conn) {
	wsc := NewWSStream(conn)
	defer wsc.CloseAndCleanup()
	err := wsc.Write(0, []byte(stdinMsg))
	assert.NoError(t, err)
	val, err := readChanTimeout(wsc.ReadChan(1), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, stdoutMsg, string(val))
	val, err = readChanTimeout(wsc.ReadChan(2), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, stderrMsg, string(val))
	time.Sleep(150 * time.Millisecond)
}

func TestWSStream(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(makeWSHandler(t)))
	u := url.URL{Scheme: "ws", Host: s.Listener.Addr().String(), Path: "/foo"}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	assert.NoError(t, err)

	runClient(t, c)
}
