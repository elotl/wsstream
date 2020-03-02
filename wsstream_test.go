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

package wsstream

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

func readChanTimeout(c <-chan []byte, t time.Duration) (int, string, error) {
	select {
	case f := <-c:
		c, m, err := UnpackMessage(f)
		return c, string(m), err
	case <-time.After(t):
		return 0, "", fmt.Errorf("timeout")
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
	c, val, err := readChanTimeout(ws.ReadMsg(), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, StdinChan, c)
	assert.Equal(t, stdinMsg, val)
	err = ws.WriteMsg(StdoutChan, []byte(stdoutMsg))
	assert.NoError(t, err)
	err = ws.WriteMsg(StderrChan, []byte(stderrMsg))
	assert.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
}

func runClient(t *testing.T, conn *websocket.Conn) {
	wsc := NewWSStream(conn)
	defer wsc.CloseAndCleanup()
	err := wsc.WriteMsg(StdinChan, []byte(stdinMsg))
	assert.NoError(t, err)
	c, val, err := readChanTimeout(wsc.ReadMsg(), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, StdoutChan, c)
	assert.Equal(t, stdoutMsg, val)
	c, val, err = readChanTimeout(wsc.ReadMsg(), 3*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, StderrChan, c)
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
