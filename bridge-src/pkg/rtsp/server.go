package rtsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"tuya-ipc-terminal/pkg/core"
	"tuya-ipc-terminal/pkg/storage"
	"tuya-ipc-terminal/pkg/tuya"
)

type RTSPServer struct {
	port           int
	listener       net.Listener
	storageManager *storage.StorageManager
	clients        map[string]*RTSPClient
	streams        map[string]*CameraStream
	failedCameras  map[string]bool
	mutex          sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	running        bool
}

type RTSPClient struct {
	conn                 net.Conn
	session              string
	cameraPath           string
	stream               *CameraStream
	reader               *bufio.Reader
	transportMode        TransportMode
	videoRTPPort         int
	videoRTCPPort        int
	audioRTPPort         int
	audioRTCPPort        int
	backAudioRTPPort     int // server-side port for back audio
	backAudioRTCPPort    int // server-side port for back audio RTCP
	videoRTPChannel      byte
	videoRTCPChannel     byte
	audioRTPChannel      byte
	audioRTCPChannel     byte
	backAudioRTPChannel  byte
	backAudioRTCPChannel byte
	setupCount           int
}

type CameraStream struct {
	camera       *storage.CameraInfo
	resolution   string
	user         *storage.UserSession
	webrtcBridge *WebRTCBridge
	clients      map[string]*RTSPClient
	mutex        sync.RWMutex
	connecting   bool
	active       bool
	lastActivity time.Time

	// Delayed shutdown
	shutdownTimer *time.Timer
	stopped       bool
	startOnce     sync.Once
	stopOnce      sync.Once
	shutdownDelay time.Duration

	// Reference to server for cleanup
	server   *RTSPServer
	streamId string
}

type ServerConfig struct {
	Port                 int
	MaxClients           int
	StreamTimeout        time.Duration
	ConnectionTimeout    time.Duration
	EnableAuthentication bool
}

func NewRTSPServer(port int, storageManager *storage.StorageManager) *RTSPServer {
	ctx, cancel := context.WithCancel(context.Background())

	return &RTSPServer{
		port:           port,
		storageManager: storageManager,
		clients:        make(map[string]*RTSPClient),
		streams:        make(map[string]*CameraStream),
		failedCameras:  make(map[string]bool),
		ctx:            ctx,
		cancel:         cancel,
		running:        false,
	}
}

func (s *RTSPServer) Start() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.running {
		return errors.New("server is already running")
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %v", s.port, err)
	}

	s.listener = listener
	s.running = true

	core.Logger.Info().Msgf("RTSP Server started on port %d", s.port)
	core.Logger.Info().Msgf("Available endpoints:")

	// List available camera endpoints
	if err := s.printAvailableEndpoints(); err != nil {
		core.Logger.Warn().Msgf("Could not list camera endpoints: %v", err)
	}

	// Start accepting connections
	go s.acceptConnections()

	// Start cleanup routine
	go s.cleanupRoutine()

	return nil
}

func (s *RTSPServer) Stop() error {
	s.mutex.Lock()
	if !s.running {
		s.mutex.Unlock()
		return errors.New("server is not running")
	}
	s.running = false
	s.cancel()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	clients := make([]*RTSPClient, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	streams := make([]*CameraStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mutex.Unlock()
	for _, client := range clients {
		_ = client.conn.Close()
	}
	for _, stream := range streams {
		stream.Stop()
	}
	return nil
}

func (s *RTSPServer) IsRunning() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.running
}

func (s *RTSPServer) GetPort() int {
	return s.port
}

func (s *RTSPServer) GetStats() ServerStats {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	activeStreams := 0
	for _, stream := range s.streams {
		stream.mutex.RLock()
		if stream.active {
			activeStreams++
		}
		stream.mutex.RUnlock()
	}

	return ServerStats{
		Port:         s.port,
		Running:      s.running,
		ClientCount:  len(s.clients),
		StreamCount:  activeStreams,
		TotalStreams: len(s.streams),
	}
}

type ServerStats struct {
	Port         int  `json:"port"`
	Running      bool `json:"running"`
	ClientCount  int  `json:"clientCount"`
	StreamCount  int  `json:"activeStreamCount"`
	TotalStreams int  `json:"totalStreams"`
}

func (s *RTSPServer) acceptConnections() {
	for s.IsRunning() {
		select {
		case <-s.ctx.Done():
			return
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				if s.IsRunning() {
					core.Logger.Error().Err(err).Msg("Error accepting connection")
				}
				continue
			}

			// Handle connection in goroutine
			go s.handleConnection(conn)
		}
	}
}

func (s *RTSPServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	session := generateSessionID()
	core.Logger.Info().Msgf("New RTSP connection established, session=%s", session)

	reader := bufio.NewReader(conn)

	// Parse initial RTSP request
	request, err := s.parseRTSPRequestFromReader(reader)
	if err != nil {
		core.Logger.Error().Err(err).Msg("Error parsing initial RTSP request")
		return
	}

	// Extract camera path from URL
	cameraPath, streamResolution := extractCameraPath(request.URL)
	if cameraPath == "" {
		core.Logger.Error().Msg("Invalid RTSP URL")
		sendRTSPResponse(conn, 400, "Bad Request", nil, "")
		return
	}

	// Find camera
	camera, user, err := s.findCamera(cameraPath)
	if err != nil {
		core.Logger.Error().Msgf("Error finding camera for path %s: %v", cameraPath, err)
		sendRTSPResponse(conn, 500, "Internal Server Error", nil, "")
		return
	}

	if camera == nil {
		core.Logger.Error().Msgf("Camera not found for path %s", cameraPath)
		sendRTSPResponse(conn, 404, "Not Found", nil, "")
		return
	}

	core.Logger.Info().Msgf("New RTSP connection for camera: %s (%s)", camera.DeviceName, camera.DeviceID)

	// Create or get existing stream
	stream, err := s.getOrCreateStream(camera, streamResolution, user)
	if err != nil {
		core.Logger.Error().Err(err).Msgf("Failed to create stream for camera %s", camera.DeviceName)
		sendRTSPResponse(conn, 500, "Internal Server Error", nil, "Failed to create stream")
		return
	}

	// Create RTSP client
	client := &RTSPClient{
		conn:                conn,
		reader:              reader,
		session:             session,
		cameraPath:          cameraPath,
		stream:              stream,
		transportMode:       TransportUDP, // Default to UDP
		videoRTPPort:        0,
		audioRTPPort:        0,
		backAudioRTPPort:    0,
		videoRTPChannel:     0,
		audioRTPChannel:     2,
		backAudioRTPChannel: 4,
		setupCount:          0,
	}

	// Add client to server and stream
	s.addClient(client)
	if !stream.AddClient(client) {
		s.removeClient(client.session)
		return
	}

	// Handle initial request
	s.handleRTSPMethod(client, request)

	// Handle further requests
	s.handleRTSPProtocol(client)
}

func (s *RTSPServer) findCamera(path string) (*storage.CameraInfo, *storage.UserSession, error) {
	cameras, err := s.storageManager.GetAllCameras()
	if err != nil {
		return nil, nil, err
	}

	// Find camera by RTSP path
	for _, camera := range cameras {
		if camera.RTSPPath == path {
			// Get user for this camera
			users, err := s.storageManager.ListUsers()
			if err != nil {
				continue
			}

			for _, user := range users {
				if user.UserKey == camera.UserKey {
					return &camera, &user, nil
				}
			}
		}
	}

	return nil, nil, nil
}

func (s *RTSPServer) getOrCreateStream(camera *storage.CameraInfo, resolution string, user *storage.UserSession) (*CameraStream, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.ctx.Err() != nil {
		return nil, s.ctx.Err()
	}
	if s.failedCameras[camera.DeviceID] {
		return nil, errors.New("camera streaming blocked after an error; reload the integration to retry")
	}
	id := fmt.Sprintf("%s-%s", camera.DeviceID, resolution)
	for _, stream := range s.streams {
		if stream.camera.DeviceID == camera.DeviceID {
			if stream.streamId == id {
				return stream, nil
			}
			return nil, errors.New("another quality is already using this camera; close it first")
		}
	}
	stream := NewCameraStream(camera, resolution, user, s.storageManager, s)
	stream.webrtcBridge.OnError = func(err error) { s.failStream(stream, err) }
	s.streams[id] = stream
	return stream, nil
}

func (s *RTSPServer) failStream(stream *CameraStream, err error) {
	s.mutex.Lock()
	s.failedCameras[stream.camera.DeviceID] = true
	s.mutex.Unlock()
	core.Logger.Error().Err(err).Msg("Camera session failed; automatic camera reconnect is blocked until integration reload")
	stream.Stop()
}

func (s *RTSPServer) removeStream(stream *CameraStream) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	// An old shutdown must never remove a newer replacement session.
	if s.streams[stream.streamId] == stream {
		delete(s.streams, stream.streamId)
	}
}

func (s *RTSPServer) addClient(client *RTSPClient) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.clients[client.session] = client
}

func (s *RTSPServer) removeClient(id string) {
	s.mutex.Lock()
	client := s.clients[id]
	delete(s.clients, id)
	s.mutex.Unlock()
	if client != nil {
		if client.stream != nil {
			client.stream.RemoveClient(id)
		}
		_ = client.conn.Close()
	}
}

func (s *RTSPServer) printAvailableEndpoints() error {
	cameras, err := s.storageManager.GetAllCameras()
	if err != nil {
		return err
	}

	if len(cameras) == 0 {
		core.Logger.Warn().Msg("  No cameras available. Run 'cameras refresh' first.")
		return nil
	}

	for _, camera := range cameras {
		var skill *tuya.Skill
		json.Unmarshal([]byte(camera.Skill), &skill)

		supportClarity := skill != nil && (skill.WebRTC&(1<<5)) != 0
		baseUrl := fmt.Sprintf("rtsp://localhost:%d%s", s.port, camera.RTSPPath)

		if supportClarity {
			core.Logger.Info().Msgf("  %s/hd (%s)", baseUrl, camera.DeviceName)
			core.Logger.Info().Msgf("  %s/sd (%s)", baseUrl, camera.DeviceName)
		} else {
			core.Logger.Info().Msgf("  %s (%s)", baseUrl, camera.DeviceName)
		}
	}

	return nil
}

func (s *RTSPServer) cleanupRoutine() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.cleanupInactiveStreams()
		}
	}
}

func (s *RTSPServer) cleanupInactiveStreams() {
	s.mutex.RLock()
	streams := make([]*CameraStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mutex.RUnlock()
	for _, stream := range streams {
		stream.mutex.Lock()
		stale := time.Since(stream.lastActivity) > 5*time.Minute && len(stream.clients) == 0
		if stale {
			stream.stopped = true
		}
		stream.mutex.Unlock()
		if stale {
			stream.Stop()
		}
	}
}

func NewCameraStream(camera *storage.CameraInfo, resolution string, user *storage.UserSession, storageManager *storage.StorageManager, server *RTSPServer) *CameraStream {
	stream := &CameraStream{
		camera:        camera,
		resolution:    resolution,
		user:          user,
		clients:       make(map[string]*RTSPClient),
		active:        false,
		lastActivity:  time.Now(),
		shutdownDelay: 5 * time.Second,
		server:        server,
		streamId:      fmt.Sprintf("%s-%s", camera.DeviceID, resolution),
	}

	stream.webrtcBridge = NewWebRTCBridge(camera, resolution, user, storageManager)

	return stream
}

func (cs *CameraStream) AddClient(client *RTSPClient) bool {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	if cs.stopped {
		return false
	}
	if cs.shutdownTimer != nil {
		cs.shutdownTimer.Stop()
		cs.shutdownTimer = nil
	}
	cs.clients[client.session] = client
	cs.lastActivity = time.Now()
	cs.startOnce.Do(func() { cs.connecting = true; go cs.startStream() })
	return true
}

func (cs *CameraStream) RemoveClient(id string) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.webrtcBridge.rtpForwarder.RemoveClient(id)
	delete(cs.clients, id)
	cs.lastActivity = time.Now()
	// Also shut down when the final viewer leaves during the handshake.
	if len(cs.clients) == 0 && !cs.stopped {
		cs.scheduleShutdown()
	}
}

func (cs *CameraStream) SetShutdownDelay(delay time.Duration) {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.shutdownDelay = delay
}

func (cs *CameraStream) Stop() {
	cs.stopOnce.Do(func() {
		cs.mutex.Lock()
		cs.stopped, cs.active, cs.connecting = true, false, false
		if cs.shutdownTimer != nil {
			cs.shutdownTimer.Stop()
			cs.shutdownTimer = nil
		}
		clients := make([]*RTSPClient, 0, len(cs.clients))
		for _, client := range cs.clients {
			clients = append(clients, client)
		}
		cs.clients = make(map[string]*RTSPClient)
		cs.mutex.Unlock()
		for _, client := range clients {
			_ = client.conn.Close()
			if cs.server != nil {
				cs.server.removeClient(client.session)
			}
		}
		cs.webrtcBridge.Stop()
		if cs.server != nil {
			cs.server.removeStream(cs)
		}
	})
}

func (cs *CameraStream) startStream() {
	if err := cs.webrtcBridge.Start(); err != nil {
		cs.mutex.RLock()
		stopped := cs.stopped
		cs.mutex.RUnlock()
		if !stopped && cs.server != nil {
			cs.server.failStream(cs, err)
		} else {
			cs.Stop()
		}
		return
	}
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	if !cs.stopped {
		cs.connecting, cs.active = false, true
	}
}

func (cs *CameraStream) isActive() bool {
	cs.mutex.RLock()
	defer cs.mutex.RUnlock()
	return cs.active
}

// Called with cs.mutex held. Mark stopped before releasing it, so an arriving
// viewer cannot cancel an already-running shutdown and reuse a closed bridge.
func (cs *CameraStream) scheduleShutdown() {
	if cs.shutdownTimer != nil {
		cs.shutdownTimer.Stop()
	}
	cs.shutdownTimer = time.AfterFunc(cs.shutdownDelay, func() {
		cs.mutex.Lock()
		stop := len(cs.clients) == 0 && !cs.stopped
		if stop {
			cs.stopped = true
		}
		cs.shutdownTimer = nil
		cs.mutex.Unlock()
		if stop {
			cs.Stop()
		}
	})
}
