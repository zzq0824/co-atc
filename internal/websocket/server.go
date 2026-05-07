package websocket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/yegors/co-atc/pkg/logger"
)

// 飞行器流式传输的新消息类型
const (
	MessageTypeAircraftAdded           = "aircraft_added"
	MessageTypeAircraftUpdate          = "aircraft_update"
	MessageTypeAircraftRemoved         = "aircraft_removed"
	MessageTypeAircraftPredictedState  = "aircraft_predicted_state"
	MessageTypeAircraftBulkRequest     = "aircraft_bulk_request"     // 客户端请求批量数据
	MessageTypeAircraftBulkResponse    = "aircraft_bulk_response"    // 服务器发送批量数据
	MessageTypeFilterUpdate            = "filter_update"             // 客户端发送过滤偏好
	MessageTypeSimulationControlUpdate = "simulation_control_update" // 客户端更新模拟控制
	MessageTypeFrequencyStatus         = "frequency_status"          // 频率连接状态变化
)

// Message 表示一条 WebSocket 消息
type Message struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

// AircraftBulkRequest 表示客户端对批量飞行器数据的请求
type AircraftBulkRequest struct {
	Filters map[string]interface{} `json:"filters"` // 过滤参数
}

// MessageHandler 定义处理 WebSocket 入站消息的接口
type MessageHandler interface {
	HandleMessage(client *Client, messageType string, data map[string]interface{}) error
}

// Client 表示一个 WebSocket 客户端
type Client struct {
	conn      *websocket.Conn
	send      chan *Message
	server    *Server
	mu        sync.Mutex
	closed    bool
	closeChan chan struct{}
}

// Server 表示一个 WebSocket 服务器
type Server struct {
	clients        map[*Client]bool
	register       chan *Client
	unregister     chan *Client
	broadcast      chan *Message
	upgrader       websocket.Upgrader
	logger         *logger.Logger
	mu             sync.RWMutex
	messageHandler MessageHandler // 入站消息的处理器
}

// NewServer 创建一个新的 WebSocket 服务器
func NewServer(logger *logger.Logger) *Server {
	return &Server{
		clients:    make(map[*Client]bool),
		register:   make(chan *Client, 32),   // 带缓冲以防止 goroutine 阻塞
		unregister: make(chan *Client, 32),   // 带缓冲以防止 goroutine 阻塞
		broadcast:  make(chan *Message, 512), // 带缓冲用于高吞吐广播
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // 允许所有来源
			},
		},
		logger: logger.Named("web-socket"),
	}
}

// SetMessageHandler 为入站 WebSocket 消息设置消息处理器
func (s *Server) SetMessageHandler(handler MessageHandler) {
	s.messageHandler = handler
}

// Run 启动 WebSocket 服务器
func (s *Server) Run() {
	s.logger.Info("正在启动 WebSocket 服务器")

	for {
		select {
		case client := <-s.register:
			s.mu.Lock()
			s.clients[client] = true
			clientCount := len(s.clients)
			s.mu.Unlock()
			s.logger.Debug("客户端已注册", String("client_count", fmt.Sprintf("%d", clientCount)))

		case client := <-s.unregister:
			s.mu.Lock()
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				// 先将客户端标记为已关闭,以防止新消息
				client.mu.Lock()
				client.closed = true
				client.mu.Unlock()
				// 然后关闭通道
				close(client.send)
			}
			clientCount := len(s.clients)
			s.mu.Unlock()
			s.logger.Debug("客户端已注销", String("client_count", fmt.Sprintf("%d", clientCount)))

		case message := <-s.broadcast:
			s.mu.RLock()
			clientsToRemove := make([]*Client, 0)
			for client := range s.clients {
				// 在发送前检查客户端是否仍然有效
				client.mu.Lock()
				if client.closed {
					clientsToRemove = append(clientsToRemove, client)
					client.mu.Unlock()
					continue
				}
				client.mu.Unlock()

				// 向所有客户端发送 - 过滤在客户端完成
				select {
				case client.send <- message:
					// 消息发送成功
				default:
					// 通道已满,标记为待移除
					clientsToRemove = append(clientsToRemove, client)
				}
			}
			s.mu.RUnlock()

			// 清理失败的客户端
			if len(clientsToRemove) > 0 {
				s.mu.Lock()
				for _, client := range clientsToRemove {
					if _, ok := s.clients[client]; ok {
						delete(s.clients, client)
						client.mu.Lock()
						if !client.closed {
							client.closed = true
							close(client.send)
						}
						client.mu.Unlock()
					}
				}
				s.mu.Unlock()
			}
		}
	}
}

// HandleConnection 处理一个 WebSocket 连接
func (s *Server) HandleConnection(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("正在处理新的 WebSocket 连接请求",
		String("remote_addr", r.RemoteAddr),
		String("user_agent", r.UserAgent()))

	// 将 HTTP 连接升级为 WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("升级连接失败",
			Error(err),
			String("remote_addr", r.RemoteAddr))
		return
	}

	s.logger.Debug("成功将连接升级为 WebSocket",
		String("remote_addr", r.RemoteAddr))

	// 创建客户端
	client := &Client{
		conn:      conn,
		send:      make(chan *Message, 256),
		server:    s,
		closeChan: make(chan struct{}),
	}

	// 注册客户端
	s.register <- client

	// 启动客户端 goroutine
	go client.readPump()
	go client.writePump()
}

// Broadcast 向所有已连接的客户端发送一条消息
func (s *Server) Broadcast(message *Message) {
	//s.logger.Debug("正在向所有客户端广播消息",
	//	String("message_type", message.Type),
	//	String("client_count", fmt.Sprintf("%d", len(s.clients))))

	// 飞行器移动更新应立即分发(无服务器端排队)
	if message.Type == MessageTypeAircraftAdded || message.Type == MessageTypeAircraftUpdate || message.Type == MessageTypeAircraftRemoved || message.Type == MessageTypeAircraftPredictedState {
		s.broadcastImmediate(message)
		return
	}

	s.broadcast <- message
}

// broadcastImmediate 不通过广播队列,直接将消息发送给所有客户端。
func (s *Server) broadcastImmediate(message *Message) {
	s.mu.RLock()
	clientsToRemove := make([]*Client, 0)
	for client := range s.clients {
		client.mu.Lock()
		if client.closed {
			clientsToRemove = append(clientsToRemove, client)
			client.mu.Unlock()
			continue
		}
		client.mu.Unlock()

		if !client.SendMessage(message) {
			clientsToRemove = append(clientsToRemove, client)
		}
	}
	s.mu.RUnlock()

	if len(clientsToRemove) > 0 {
		s.mu.Lock()
		for _, client := range clientsToRemove {
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				client.mu.Lock()
				if !client.closed {
					client.closed = true
					close(client.send)
				}
				client.mu.Unlock()
			}
		}
		s.mu.Unlock()
	}
}

// readPump 将消息从 WebSocket 连接传输到 hub
func (c *Client) readPump() {
	defer func() {
		c.mu.Lock()
		if !c.closed {
			c.closed = true
		}
		c.mu.Unlock()

		c.server.unregister <- c
		c.conn.Close()
	}()

	for {
		// 检查客户端是否已关闭
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			break
		}
		c.mu.Unlock()

		// 读取消息
		_, messageBytes, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				c.server.logger.Error("WebSocket 读取错误", Error(err))
			}
			break
		}

		// 解析入站消息
		var message struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
		}

		if err := json.Unmarshal(messageBytes, &message); err != nil {
			c.server.logger.Error("解析 WebSocket 消息失败", Error(err))
			continue
		}

		c.server.logger.Debug("已接收 WebSocket 消息",
			String("type", message.Type),
			String("client", c.conn.RemoteAddr().String()))

		// 如果设置了处理器,处理消息
		if c.server.messageHandler != nil {
			if err := c.server.messageHandler.HandleMessage(c, message.Type, message.Data); err != nil {
				c.server.logger.Error("处理 WebSocket 消息失败",
					Error(err),
					String("type", message.Type))
			}
		}
	}
}

// writePump 将消息从 hub 传输到 WebSocket 连接
func (c *Client) writePump() {
	defer func() {
		c.mu.Lock()
		if !c.closed {
			c.closed = true
		}
		c.mu.Unlock()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				// 通道已关闭
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				c.mu.Unlock()
				return
			}

			// 将消息序列化为 JSON
			data, err := json.Marshal(message)
			if err != nil {
				c.server.logger.Error("序列化消息失败", Error(err))
				c.mu.Unlock()
				continue
			}

			// 写入消息
			//c.server.logger.Debug("正在向客户端发送消息",
			//	String("message_type", message.Type),
			//	String("message_length", fmt.Sprintf("%d bytes", len(data))))

			w.Write(data)

			// 关闭 writer
			if err := w.Close(); err != nil {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()

		case <-c.closeChan:
			return
		}
	}
}

// Close 关闭客户端连接
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	c.closed = true
	close(c.closeChan)
	c.conn.Close()
}

// SendMessage 向该特定客户端发送消息
func (c *Client) SendMessage(message *Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 检查客户端是否已关闭
	if c.closed {
		return false
	}

	// 使用非阻塞 select 尝试发送消息
	select {
	case c.send <- message:
		return true
	default:
		// 通道已满,丢弃消息
		return false
	}
}

// 导入 logger 函数
var (
	String = logger.String
	Error  = logger.Error
)
