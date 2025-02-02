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
	"bytes"
	"io"
	"log"
)

type WSReadWriter struct {
	*WSStream
	readers map[int]*WSReader
}

type WSReader struct {
	msgChan chan []byte
	readBuf bytes.Buffer
	closed  <-chan struct{}
}

type WSWriter struct {
	*WSStream
	WriteChan int
}

func (ws *WSReadWriter) CreateWriter(channel int) *WSWriter {
	return &WSWriter{
		WSStream:  ws.WSStream,
		WriteChan: channel,
	}
}

func (ws *WSWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 {
		err = ws.WriteMsg(ws.WriteChan, p)
	}
	return len(p), err
}

func (ws *WSReadWriter) CreateReader(channel int) *WSReader {
	rb := &WSReader{
		msgChan: make(chan []byte, wsBufSize),
		closed:  ws.Closed(),
	}
	if ws.readers == nil {
		ws.readers = make(map[int]*WSReader)
	}
	ws.readers[channel] = rb
	return rb
}

func (ws *WSReadWriter) RunDispatch() {
	for {
		select {
		case <-ws.Closed():
			return
		case msg := <-ws.ReadMsg():
			c, msg, err := UnpackMessage(msg)
			if err != nil {
				log.Printf("Error unpacking websocket msg: %v", err)
			}
			ws.doDispatch(c, msg)
		}
	}
}

func (ws *WSReadWriter) doDispatch(channel int, msg []byte) {
	rb, exists := ws.readers[channel]
	if !exists {
		return
	}
	rb.msgChan <- msg
}

func (r *WSReader) IsClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

func (r *WSReader) Read(p []byte) (n int, err error) {
	if r.readBuf.Len() > 0 {
		return r.readBuf.Read(p)
	}
	select {
	case <-r.closed:
		// drain the read channel.  Not sure if we need to do this
		// anymore now that we're no longer buffering channels...
		select {
		case msg := <-r.msgChan:
			numCopied := copy(p, msg)
			if numCopied < len(msg) {
				_, _ = r.readBuf.Write(msg[numCopied:])
			}
			return numCopied, nil
		default:
			return 0, io.EOF
		}
	case msg := <-r.msgChan:
		numCopied := copy(p, msg)
		if numCopied < len(msg) {
			_, _ = r.readBuf.Write(msg[numCopied:])
		}
		return numCopied, nil
	}
}
