package rtsp

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
	"tuya-ipc-terminal/pkg/storage"
)

func cameraFixture() *storage.CameraInfo {
	return &storage.CameraInfo{DeviceID: "test-camera", DeviceName: "Test doorbell"}
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func bridgeWithResources(t *testing.T) *WebRTCBridge {
	t.Helper()
	bridge := NewWebRTCBridge(cameraFixture(), "hd", nil, nil)
	pc, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	bridge.peerConnection = pc
	if err := bridge.rtpForwarder.AddUDPClient("test-viewer", 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	return bridge
}

func TestStopCleansResourcesEvenWhenNotConnected(t *testing.T) {
	bridge := bridgeWithResources(t)
	var workers sync.WaitGroup
	for i := 0; i < 10; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); bridge.Stop() }()
	}
	workers.Wait()
	if bridge.peerConnection.ConnectionState() != pion.PeerConnectionStateClosed {
		t.Fatal("peer connection leaked")
	}
	if bridge.rtpForwarder.GetClientCount() != 0 {
		t.Fatal("RTP clients leaked")
	}
	if bridge.ctx.Err() == nil {
		t.Fatal("context not cancelled")
	}
}

func TestStartFailureCleansPartiallyInitializedBridge(t *testing.T) {
	bridge := bridgeWithResources(t)
	if err := bridge.Start(); err == nil {
		t.Fatal("missing user session must fail")
	}
	if bridge.peerConnection.ConnectionState() != pion.PeerConnectionStateClosed {
		t.Fatal("startup failure leaked peer connection")
	}
}

func TestRemoteDisconnectNotifiesOnceAndClosesResources(t *testing.T) {
	bridge := bridgeWithResources(t)
	var calls atomic.Int32
	bridge.OnError = func(error) { calls.Add(1) }
	bridge.handleError(errors.New("camera client disconnected"))
	bridge.handleError(errors.New("duplicate disconnect"))
	await(t, func() bool { return bridge.peerConnection.ConnectionState() == pion.PeerConnectionStateClosed })
	if calls.Load() != 1 {
		t.Fatalf("error callback called %d times", calls.Load())
	}
}

func TestFailureWithActiveViewerClosesAndBlocksRetries(t *testing.T) {
	server := NewRTSPServer(0, nil)
	stream, err := server.getOrCreateStream(cameraFixture(), "hd", nil)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	client := &RTSPClient{conn: left, session: "viewer", stream: stream}
	server.clients[client.session] = client
	stream.clients[client.session] = client
	stream.active = true
	server.failStream(stream, errors.New("camera client disconnected"))
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := right.Read(make([]byte, 1)); err == nil {
		t.Fatal("viewer socket left open")
	}
	for i := 0; i < 100; i++ {
		if _, err := server.getOrCreateStream(cameraFixture(), "hd", nil); err == nil {
			t.Fatal("retry opened another camera session")
		}
	}
	if len(server.streams) != 0 || len(server.clients) != 0 {
		t.Fatal("failed stream remains registered")
	}
	fresh := NewRTSPServer(0, nil)
	if _, err := fresh.getOrCreateStream(cameraFixture(), "hd", nil); err != nil {
		t.Fatal("fresh server must allow a manual retry")
	}
}

func TestOnlyOneQualityCanUseCamera(t *testing.T) {
	server := NewRTSPServer(0, nil)
	first, err := server.getOrCreateStream(cameraFixture(), "hd", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := server.getOrCreateStream(cameraFixture(), "hd", nil)
	if err != nil || again != first {
		t.Fatal("same quality must share one source")
	}
	if _, err := server.getOrCreateStream(cameraFixture(), "sd", nil); err == nil {
		t.Fatal("second quality must not open a competing session")
	}
	first.Stop()
	if _, err := server.getOrCreateStream(cameraFixture(), "sd", nil); err != nil {
		t.Fatal("normal close must allow another quality later")
	}
}

func TestLastViewerLeavingDuringHandshakeStopsStream(t *testing.T) {
	server := NewRTSPServer(0, nil)
	stream, _ := server.getOrCreateStream(cameraFixture(), "hd", nil)
	stream.startOnce.Do(func() {}) // Simulate a pending handshake without cloud I/O.
	stream.connecting = true
	stream.SetShutdownDelay(time.Millisecond)
	left, right := net.Pipe()
	defer right.Close()
	client := &RTSPClient{conn: left, session: "viewer", stream: stream}
	if !stream.AddClient(client) {
		t.Fatal("viewer refused")
	}
	stream.RemoveClient(client.session)
	await(t, func() bool { server.mutex.RLock(); defer server.mutex.RUnlock(); return len(server.streams) == 0 })
	if stream.webrtcBridge.ctx.Err() == nil {
		t.Fatal("pending handshake was not cancelled")
	}
	_ = left.Close()
}

func TestOldShutdownDoesNotRemoveNewStream(t *testing.T) {
	server := NewRTSPServer(0, nil)
	old := NewCameraStream(cameraFixture(), "hd", nil, nil, server)
	fresh := NewCameraStream(cameraFixture(), "hd", nil, nil, server)
	server.streams[fresh.streamId] = fresh
	old.Stop()
	if server.streams[fresh.streamId] != fresh {
		t.Fatal("old cleanup deleted replacement stream")
	}
	fresh.Stop()
}
