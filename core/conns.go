//   Copyright 2013 Vastech SA (PTY) LTD
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package core

import (
	"errors"
	"fmt"
	"github.com/jsgilmore/gostorm/messages"
	"os"
	"path/filepath"
	"strconv"
)

// BoltConn is the interface that implements the possible bolt actions
type BoltConn interface {
	Connect()
	Context() *messages.Context
	Log(msg string)
	ReadTuple(contentStructs ...interface{}) (meta *messages.TupleMetadata, err error)
	SendAck(id string)
	SendFail(id string)
	Emit(anchors []string, stream string, content ...interface{}) (taskIds []int32)
	EmitDirect(anchors []string, stream string, directTask int64, contents ...interface{})
}

// SpoutConn is the interface that implements the possible spout actions
type SpoutConn interface {
	Connect()
	Context() *messages.Context
	Log(msg string)
	ReadSpoutMsg() (command, id string, err error)
	SendSync()
	Emit(id string, stream string, contents ...interface{}) (taskIds []int32)
	EmitDirect(id string, stream string, directTask int64, contents ...interface{})
}

// newStormConn creates a new generic Storm connection
// This connection must be embedded in either a spout or bolt
func newStormConn(in Input, out Output) *stormConnImpl {
	stormConn := &stormConnImpl{
		Input:  in,
		Output: out,
	}
	return stormConn
}

// stormConnImpl represents the common functions that both a bolt and spout are capable of
type stormConnImpl struct {
	Input
	Output
	context *messages.Context
}

func (this *stormConnImpl) readContext() (context *messages.Context, err error) {
	context = &messages.Context{}
	err = this.ReadMsg(context)
	if err != nil {
		return nil, err
	}
	return context, nil
}

func (this *stormConnImpl) reportPid() {
	// Send the pid to Storm
	msg := &messages.Pid{
		Pid: int32(os.Getpid()),
	}
	this.SendMsg(msg)

	// Write an empty file with the pid, which storm can use to kill our process
	pidFile, err := os.Create(filepath.Join(this.Context().PidDir, strconv.Itoa(os.Getpid())))
	if err != nil {
		panic(err)
	}
	err = pidFile.Close()
	if err != nil {
		panic(err)
	}
}

// Initialise set the storm input reader to the specified file
// descriptor, reads the topology configuration for Storm and reports
// the pid to Storm
func (this *stormConnImpl) Connect() {
	// Receive the topology layout and config
	var err error
	this.context, err = this.readContext()
	if err != nil {
		panic(fmt.Sprintf("Storm failed to initialise: %v", err))
	}

	this.reportPid()
}

func (this *stormConnImpl) Context() *messages.Context {
	return this.context
}

// Log sends a log message that will be logged by Storm
func (this *stormConnImpl) Log(text string) {
	this.EmitGeneric("log", "", "", text, nil, 0)
}

// NewBoltConn returns a Storm bolt connection that a Go bolt can use to communicate with Storm
func NewBoltConn(in Input, out Output) BoltConn {
	boltConn := &boltConnImpl{
		stormConnImpl: newStormConn(in, out),
	}
	return boltConn
}

type boltConnImpl struct {
	*stormConnImpl
}

func newTupleMetadata(id, comp, stream string, task int64) *messages.TupleMetadata {
	meta := &messages.TupleMetadata{
		Id:     id,
		Comp:   comp,
		Stream: stream,
		Task:   task,
	}
	return meta
}

// SendAck acks the received message id
// SendAck has to be called after an emission anchored to the acked id,
// otherwise Storm will report an error.
func (this *boltConnImpl) SendAck(id string) {
	this.EmitGeneric("ack", id, "", "", nil, 0)
}

// SendFail reports that the message with the given Id failed
// No emission should be anchored to a failed message Id
func (this *boltConnImpl) SendFail(id string) {
	this.EmitGeneric("fail", id, "", "", nil, 0)
}

// Emit emits a tuple with the given array of interface{}s as values,
// anchored to the given array of taskIds, sent out on the given stream.
// A stream value of "" or "default" can be used to denote the default stream
// The function returns a list of taskIds to which the message was sent.
func (this *boltConnImpl) Emit(anchors []string, stream string, contents ...interface{}) (taskIds []int32) {
	this.EmitDirect(anchors, stream, 0, contents...)
	return this.ReadTaskIds()
}

// EmitDirect emits a tuple with the given array of interface{}s as values,
// anchored to the given array of taskIds, sent out on the given stream,
// to the given taskId.
// The topology should have been configured for direct transmission
// for this call to work.
// A stream value of "" or "default" can be used to denote the default stream
// The function returns a list of taskIds to which the message was sent.
func (this *boltConnImpl) EmitDirect(anchors []string, stream string, directTask int64, contents ...interface{}) {
	this.EmitGeneric("emit", "", stream, "", anchors, directTask, contents...)
}

// NewSpoutConn returns a Storm spout connection that a Go spout can use to communicate with Storm
func NewSpoutConn(in Input, out Output) SpoutConn {
	spoutConn := &spoutConnImpl{
		stormConnImpl: newStormConn(in, out),
	}
	return spoutConn
}

type spoutConnImpl struct {
	readyToSend bool
	*stormConnImpl
}

// ReadMsg reads a message from Storm.
// The message read can be either a next, ack or fail message.
// A check is performed to verify that Storm has been initialised.
func (this *spoutConnImpl) ReadSpoutMsg() (command, id string, err error) {
	if this.context == nil {
		return "", "", errors.New("Attempting to read from uninitialised Storm connection")
	}
	this.readyToSend = true

	msg := &messages.SpoutMsg{}
	err = this.ReadMsg(msg)
	if err != nil {
		return "", "", err
	}
	return msg.Command, msg.Id, nil
}

// SendSync sends a sync message to Storm.
// After a sync message is sent, it is not possible for a spout to
// emit a message before a ReadMsg has been performed. This is to
// enforce the synchronous behaviour of a spout as required by Storm.
func (this *spoutConnImpl) SendSync() {
	this.EmitGeneric("sync", "", "", "", nil, 0)
	this.readyToSend = false
}

// Emit emits a tuple with the given array of interface{}s as values,
// with the given taskId, sent out on the given stream.
// A stream value of "" or "default" can be used to denote the default stream
// The function returns a list of taskIds to which the message was sent.
func (this *spoutConnImpl) Emit(id string, stream string, contents ...interface{}) (taskIds []int32) {
	this.EmitDirect(id, stream, 0, contents...)
	return this.ReadTaskIds()
}

// EmitDirect emits a tuple with the given array of interface{}s as values,
// with the given taskId, sent out on the given stream, to the given taskId.
// The topology should have been configured for direct transmission
// for this call to work.
// A stream value of "" or "default" can be used to denote the default stream
// The function returns a list of taskIds to which the message was sent.
func (this *spoutConnImpl) EmitDirect(id string, stream string, directTask int64, contents ...interface{}) {
	if !this.readyToSend {
		panic("Spout not ready to send")
	}
	this.EmitGeneric("emit", id, stream, "", nil, directTask, contents...)
}