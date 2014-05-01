package apns

import (
	"crypto/tls"
	"encoding/binary"
	"log"
	"time"
)

var ResponseQueueSize = 10000
var SentBufferSize = 10000

//a Connection represents a single connection to APNS.
type Connection struct {
	client Client
	conn   tls.Conn
	queue  chan PushNotification
	errors chan *BadPushNotification
}

type Response struct {
	Status     uint8
	Identifier uint32
}

func newResponse() *Response {
	return new(Response)
}

type BadPushNotification struct {
	PushNotification
	Status uint8
}

func (conn *Connection) Enqueue(pn *PushNotification) {
	go func(pn *PushNotification) {
		conn.queue <- *pn
	}(pn)
}

func (conn *Connection) Errors() (errors <-chan *BadPushNotification) {
	return conn.errors
}

func (conn *Connection) Start() error {
	//Connect to APNS. The reason this is here as well as in sender is that this probably catches any unavoidable errors in a synchronous fashion, while in sender it can reconnect after temporary errors (which should work most of the time.)
	//Start sender goroutine
	sent := make(chan PushNotification)
	go conn.sender(conn.queue, sent)
	//Start reader goroutine
	responses := make(chan *Response, ResponseQueueSize)
	go conn.reader(responses)
	//Start limbo goroutine
	return nil
}

func (conn *Connection) Stop() {
	//We can't just close the main queue channel, because retries might still need to be sent there.
	//
}

func (conn *Connection) sender(queue <-chan PushNotification, sent chan PushNotification) {
	for {
		pn, ok := <-conn.queue
		if !ok {
			//That means the connection is closed, teardown the connection (should it be this routine's responsibility?) and return
			return
		} else {
			//If not connected, connect
			//Exponential backoff up to a limit
			//Then send the push notification
			//TODO(@draaglom): Do buffering as per the APNS docs
		}
	}
}

func (conn *Connection) reader(responses chan<- *Response) {
	buffer := make([]byte, 6)
	for {
		_, err := conn.conn.Read(buffer)
		if err != nil {
			log.Println("APNS: Error reading from connection: ", err)
			conn.conn.Close()
			return
		}
		resp := newResponse()
		resp.Identifier = binary.BigEndian.Uint32(buffer[2:6])
		resp.Status = uint8(buffer[1])
		responses <- resp
	}
}

func (conn *Connection) limbo(sent <-chan PushNotification, responses chan Response, errors chan PushNotification, queue chan PushNotification) {
	limbo := make(chan PushNotification, SentBufferSize)
	ticker := time.NewTicker(1 * time.Second)
	timeNextNotification := true
	for {
		select {
		case pn := <-sent:
			//Drop it into the array
			limbo <- pn
			if timeNextNotification {
				//Is there a cleaner way of doing this?
				go func(pn PushNotification) {
					<-time.After(TimeoutSeconds)
					successResp := newResponse()
					successResp.Identifier = pn.Identifier
					responses <- *successResp
				}(pn)
				timeNextNotification = false
			}
		case resp, ok := <-responses:
			if !ok {
				//If the responses channel is closed, that means we're shutting down the connection.
				//We should
			}
			switch {
			case resp.Status == 0:
				//Status 0 is a "success" response generated by a timeout in the library.
				for pn := range limbo {
					//Drop all the notifications until we get to the timed-out one.
					//(and leave the others in limbo)
					if pn.Identifier == resp.Identifier {
						break
					}
				}
			default:
				hit := false
				for pn := range limbo {
					switch {
					case pn.Identifier != resp.Identifier && !hit:
						//We haven't seen the identified notification yet
						//so these are all successful (drop silently)
					case pn.Identifier == resp.Identifier:
						hit = true
						if resp.Status != 10 {
							//It was an error, we should report this on the error channel
						}
					case pn.Identifier != resp.Identifier && hit:
						//We've already seen the identified notification,
						//so these should be requeued
						conn.Enqueue(&pn)
					}
				}
			}
			//Drop all of the notifications before this response (they're ok)
			//if status != 10, return the offending notification on errors
			//if status == 10, close the connection.
			//requeue all the notifications after that one.
		case <-ticker.C:
			timeNextNotification = true
		}
	}
}
