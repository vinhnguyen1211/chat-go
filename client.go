// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	// "bytes"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan Message

	room string

	//
	joinRoom  chan string
	leaveRoom chan string
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		// _, message, err := c.conn.ReadMessage()
		var message Message
		err := c.conn.ReadJSON(&message)
		// s := string(message)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		// message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		// log.Println(message)

		// join room
		if message.Type == 1 {
			c.hub.registerRoom <- c
		} else if message.Type == 3 {
			c.hub.leaveRoom <- c

			// message send to private room
		} else if message.Type == 5 {
			msg := Message{Type: 6, RoomID: c.room, Message: message.Message}
			clientMessage := &ClientRoomMessage{Client: c, Message: msg}
			c.hub.broadcastRoom <- clientMessage
		} else if message.Type == 7 {
			clientMessage := &ClientRoomMessage{Client: c, Message: message}
			c.hub.joinRoom <- clientMessage
		} else if message.Type == 9 {
			// if user haven't joined any room -> quick join
			if c.room == "" {
				clientMessage := &ClientRoomMessage{Client: c, Message: message}
				c.hub.joinRoomQuickly <- clientMessage
			} else {
				msg := Message{Type: 10, Message: "you are not available for room"}
				c.send <- msg
			}

		} else {
			c.hub.broadcast <- message
		}
	}
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// w, err := c.conn.NextWriter(websocket.TextMessage)
			// if err != nil {
			// 	return
			// }
			// w.Write(message)
			err := c.conn.WriteJSON(message)

			if err != nil {
				return
			}

			// Add queued chat messages to the current websocket message.
			n := len(c.send)
			for i := 0; i < n; i++ {
				// w.Write(newline)
				// w.Write(<-c.send)
				err := c.conn.WriteJSON(message)
				if err != nil {
					return
				}
			}

			// if err := w.Close(); err != nil {
			// 	return
			// }
		case roomID, ok := <-c.joinRoom:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if roomID != "" {
				c.room = roomID

				message := Message{Type: 2, RoomID: roomID, Message: "joined room successfully"}
				err := c.conn.WriteJSON(message)
				if err != nil {
					return
				}
			}

		case roomLeaved := <-c.leaveRoom:
			if roomLeaved != "" {
				message := Message{Type: 4, RoomID: roomLeaved, Message: "leave room successfully"}
				c.room = ""
				err := c.conn.WriteJSON(message)
				if err != nil {
					return
				}
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs handles websocket requests from the peer.
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan Message, 1024),
		joinRoom:  make(chan string),
		leaveRoom: make(chan string),
	}
	log.Printf("incoming client %p", client)
	client.hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}
