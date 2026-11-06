# Session lifecycle audit

This experimental fork addresses resource leaks and connection churn in upstream
`nihatds100/DoorbellTuya` at commit `6dbe3e1`. It does not establish the cause of a
specific device firmware failure, nor guarantee video compatibility.

## Findings confirmed by source inspection

1. `WebRTCBridge.Stop()` returned immediately when `connected` was false. The
   remote-disconnect handler set exactly that flag to false. A later normal stop
   could therefore skip MQTT, PeerConnection and RTP cleanup. Startup failures
   after allocating resources had the same problem.
2. The RTSP error callback only stopped a failed source when no clients remained.
   An attached viewer could leave a dead source registered as active. Repeated
   ffmpeg attempts could cancel the five-second delayed shutdown indefinitely.
3. `Waiter.WaitChan()` returned a nil channel if completion preceded the call.
   Its unbuffered result sender could also remain blocked after a timeout.
4. The stream mutex was held across cloud negotiation. Failure callbacks could
   need that same mutex; client attachment and removal were blocked meanwhile.
5. HD and SD had separate camera session keys, so sharing through go2rtc did not
   impose a device-wide one-session limit. The integration permanently opened SD
   while HD remained separately available.
6. Unloading only stopped a privately launched go2rtc process. Streams registered
   in HA's shared go2rtc were not explicitly removed.

## Changes in this fork

- Cleanup runs exactly once, including after partial startup and remote failure.
  It attempts a session-specific MQTT disconnect, removes the MQTT camera client,
  cancels the handshake and closes Pion and RTP resources. MQTT publish waits are
  bounded. Publish acknowledgement does not prove the device released resources.
- Transport callbacks dispatch cleanup asynchronously. Network teardown happens
  outside server/stream bookkeeping locks. Closing one stream cannot delete its
  newer replacement.
- Only one quality can own a camera at a time; viewers of that quality share it.
  HA exposes and registers HD only. There is no permanent SD reader or periodic
  video probe. Old SD entities may remain unavailable in HA's entity registry.
- A fatal startup/transport error closes attached RTSP clients and blocks new
  camera sessions until the integration is explicitly reloaded. go2rtc may still
  retry the local RTSP URL, but requests are rejected without opening another Tuya
  session. MQTT automatic reconnection is disabled too.
- Leaving during a handshake also schedules teardown. A normal final-viewer
  departure keeps the existing five-second grace period.
- Unload unregisters streams owned by this integration before stopping the bridge;
  it does not stop HA's shared go2rtc. Private fallback ports are API 31984, RTSP
  38554 and WebRTC 38555 to avoid the observed 18554 collision.

## Device behavior still to investigate

A test trace showed WebRTC establishing and a remote disconnect arriving shortly
afterward, followed by local RTSP retries about every five seconds. The official
Tuya web player also failed while this integration was disabled. A firmware or
protocol issue remains possible independently of the confirmed bugs above.

The capability response reported one usable H.264 stream and another entry with
zero dimensions. The code sends a resolution command on connection and filters
TURN candidates. Neither behavior has been proved to trigger the device failure;
this patch does not change those protocol choices.

## Validation and next hardware test

From `bridge-src`: `go test -timeout=30s ./...` and `go vet ./...`.
On a supported Linux development host also run `go test -race ./...`.
From the repository root: `python -m unittest discover -s tests`.
Tests cover early/duplicate completion, partial-start cleanup, concurrent stops,
remote disconnect with a viewer, retry blocking, competing qualities, departure
during startup, stale cleanup and shared go2rtc ownership. They use synthetic
devices and do not authenticate to Tuya or contact a camera.

The Linux amd64/arm64 executables must be rebuilt from these sources; Python-only
changes cannot repair the Go cleanup paths. After deployment, restart the device
once, close other players and make one HD request. On error, verify no second
Tuya handshake occurs. Check that Tuya Smart still receives video after the HA
viewer closes. Hardware validation is required before claiming the issue fixed.
