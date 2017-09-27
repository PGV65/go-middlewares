package middlewares

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nu7hatch/gouuid"

	"github.com/gorilla/websocket"
)

/*
DataHandler - incoming request data handler
*/
type DataHandler func(string, interface{}) (interface{}, error)

/*
WebsocketServer - websocket server
*/
type WebsocketServer struct {
	Host          string
	Port          int
	Timeout       time.Duration
	Upgrader      websocket.Upgrader
	Connections   map[string]*websocket.Conn
	Subscriptions map[string]map[string]time.Time

	onConnectHandler    map[string]DataHandler
	onDisconnectHandler map[string]DataHandler
	onMessageHandler    map[string]DataHandler

	NewConnectionMessage string
	mutex                sync.Mutex
}

func (ws *WebsocketServer) Handler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	fmt.Println("New request", url)
	// query := r.URL.Query()

	segments := strings.Split(url, "?")
	route := segments[0]

	c, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		ws.handleError(err)
		return
	}
	connectionID, err := ws.newConnection(route, c)
	defer func() {
		fmt.Println("Disconnect: ", connectionID)
		ws.destroyConnection(connectionID)
		c.Close()
	}()

	for {
		mt, message, err := c.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			break
		}
		// fmt.Println("New message: ", connectionID, message)

		onMessage := ws.onMessageHandler[route]
		response, err := onMessage(connectionID, message)
		if err != nil {
			ws.handleError(ws.SendErrror(c, err.Error(), err))
			continue
		}
		err = ws.SendResponse(c, mt, "data", response)
	}
}

func (ws *WebsocketServer) Subscribe(channel, connectionID string) {
	ws.mutex.Lock()
	fmt.Println("=========== Subscribe: before ===========")
	fmt.Println("Subscriptions: ", ws.Subscriptions)
	fmt.Println("Channel: ", ws.Subscriptions[channel])
	fmt.Println("Len: ", len(ws.Subscriptions))
	ch := ws.Subscriptions[channel]

	if ch == nil {
		ch = make(map[string]time.Time, 0)
	}

	ch[connectionID] = time.Now()
	ws.Subscriptions[channel] = ch
	fmt.Println("=========== Subscribe: after ===========")
	fmt.Println("Subscriptions: ", ws.Subscriptions)
	fmt.Println("Channel: ", ws.Subscriptions[channel])
	fmt.Println("Len: ", len(ws.Subscriptions))
	ws.mutex.Unlock()
}

/*
Send - sending message (interface{}) to connectionID
*/
func (ws *WebsocketServer) Send(connectionID string, message interface{}) {
	conn := ws.Connections[connectionID]
	if conn != nil {
		ws.SendText(conn, message)
	}
}

func (ws *WebsocketServer) newConnection(route string, c *websocket.Conn) (string, error) {
	uid, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	ID := uid.String()

	ws.Connections[ID] = c

	go ws.keepAlive(ID)
	onConnect := ws.onConnectHandler[route]

	if onConnect == nil {
		fmt.Println("Connection handler is not defined")
		return ID, nil
	}

	resp, err := onConnect(ID, nil)
	if err != nil {
		ws.SendErrror(c, "onConnect error", err)
	}
	ws.SendText(c, resp)
	fmt.Println("New connection: ", ID)
	return ID, nil
}

func (ws *WebsocketServer) destroyConnection(ID string) error {
	fmt.Println("Disconnecting: ", ID, len(ws.Connections))
	fmt.Println("Connections: ", ws.Connections)
	if ws.Connections[ID] == nil {
		return nil
	}
	delete(ws.Connections, ID)
	fmt.Println("Disconnected: ", ID, len(ws.Connections))
	fmt.Println("Connections: ", ws.Connections)
	return nil
}

func (ws *WebsocketServer) OnConnect(route string, handler DataHandler) {
	if ws.onConnectHandler == nil {
		ws.onConnectHandler = make(map[string]DataHandler)
	}
	ws.onConnectHandler[route] = handler
}
func (ws *WebsocketServer) OnDisconnect(route string, handler DataHandler) {
	if ws.onDisconnectHandler == nil {
		ws.onDisconnectHandler = make(map[string]DataHandler)
	}
	ws.onDisconnectHandler[route] = handler
}
func (ws *WebsocketServer) OnMessage(route string, handler DataHandler) {
	if ws.onMessageHandler == nil {
		ws.onMessageHandler = make(map[string]DataHandler)
	}
	ws.onMessageHandler[route] = handler
}

func (ws *WebsocketServer) SendErrror(c *websocket.Conn, message string, err error) error {
	resp := make(map[string]string)
	resp["message"] = message
	resp["stack"] = err.Error()
	return ws.SendResponse(c, websocket.TextMessage, "error", resp)
}
func (ws *WebsocketServer) SendText(c *websocket.Conn, data interface{}) error {
	if err := checkConnection(c); err != nil {
		return err
	}
	return ws.SendResponse(c, websocket.TextMessage, "data", data)
}

func checkConnection(c *websocket.Conn) error {
	if c == nil {
		fmt.Println("[WS]: No listeners, or connection closed")
		return errors.New("[WS]: Connection is nil")
	}
	return nil
}

func (ws *WebsocketServer) SendResponse(c *websocket.Conn, mt int, t string, data interface{}) error {
	if err := checkConnection(c); err != nil {
		return err
	}
	d := make(map[string]interface{})
	d[t] = data
	message, err := json.Marshal(d)
	if err != nil {
		return err
	}
	ws.mutex.Lock()
	err = c.WriteMessage(mt, message)
	ws.mutex.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func (ws *WebsocketServer) handleError(err error) {
	if err != nil {
		log.Println("Error:", err)
	}
}

func (ws *WebsocketServer) Start() {
	log.Println("Starting websocket server on ", ws.getURI())
	ws.Upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	http.HandleFunc("/", ws.Handler)
	err := http.ListenAndServe(ws.getURI(), nil)
	if err != nil {
		fmt.Println("Err starting: ", err)
	}
}

func (ws *WebsocketServer) getURI() string {
	port := strconv.Itoa(ws.Port)
	return ws.Host + ":" + port
}

func (ws *WebsocketServer) keepAlive(connID string) {
	fmt.Println("Keep alive: ", connID)
	c := ws.Connections[connID]
	lastResponse := time.Now()
	c.SetPongHandler(func(msg string) error {
		lastResponse = time.Now()
		return nil
	})
	for {
		c := ws.Connections[connID]
		if c == nil {
			return
		}
		ws.mutex.Lock()
		err := c.WriteMessage(websocket.PingMessage, []byte("ping"))
		ws.mutex.Unlock()
		if err != nil {
			fmt.Println("Keepalive error:", err)
			return
		}
		time.Sleep(ws.Timeout / 10)
		if time.Now().Sub(lastResponse) > ws.Timeout {
			c.Close()
			return
		}
	}
}

func (ws *WebsocketServer) Close() error {
	return nil
}
