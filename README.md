# DoorbellTuya — experimental session lifecycle fork

This personal fork of **nihatds100/DoorbellTuya** changes the behavior described
in the original documentation below. Read the [audit](docs/session-analysis.md).
It is a development version, not a hardware-validated fix or a stable HACS release.

- HA registers and exposes **HD only, on demand**. No permanent SD reader or
  periodic video probe runs. Avoid camera preloading and live dashboard cards.
- Only one quality can own a camera session in this bridge. Multiple viewers
  share that quality through go2rtc. This cannot limit other Tuya apps or portals.
- A fatal camera-session error closes clients and blocks new Tuya sessions until
  **the integration is reloaded manually**. Local RTSP retries may continue, but
  cannot create another camera session. This intentionally replaces self-healing.
- Normal last-viewer departure closes the source after five seconds. HA or an NVR
  may itself retain a viewer longer; closure begins when its RTSP client leaves.
- Private fallback ports: API `127.0.0.1:31984`, RTSP `38554`, WebRTC `38555`.
- A **Sonnette** event entity recognizes new `initiative_message` reports with
  `cmd: ipc_doorbell` from HA's existing Tuya integration. Cached startup messages
  and duplicate deliveries are ignored. This requires the same device to be
  configured in native Tuya; it creates no extra device or video connection.
  Encrypted image data and keys are not exposed in the event attributes.

The development code and rebuilt Linux binaries are on `fix/session-lifecycle`.
Do not install only the Python files: the Go executables contain the cleanup fix.
No configuration or software on the real HA instance has been changed by this
source audit. Hardware validation remains required.

## Validate and rebuild this fork

Run `python -m unittest discover -s tests` from the repository root. From
`bridge-src`, run:

```sh
go test -timeout=30s ./...
go vet ./...
# Supported Linux host with a C compiler:
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w" -o ../custom_components/tuya_doorbell_rtsp/bin/tuya-ipc-terminal-linux-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags="-s -w" -o ../custom_components/tuya_doorbell_rtsp/bin/tuya-ipc-terminal-linux-arm64 .
```

Tests use synthetic sessions and do not contact Tuya or a camera. For a manual
development install, back up the existing integration, replace its
`custom_components/tuya_doorbell_rtsp` directory with this branch's complete
directory and restart HA. Keep credentials and runtime data out of Git. Do not
run two copies. Existing SD entities may remain unavailable in HA's registry.

Original license and attribution are retained. This fork remains for personal,
noncommercial use. The following is the **original upstream documentation**;
its always-warm, HD+SD and automatic-recovery claims do not apply to this branch.

---

# DoorbellTuya (upstream)

**Local, always-warm RTSP from a Tuya / Smart Life video doorbell — for Home Assistant.**

Many Tuya video doorbells expose no native RTSP/ONVIF, so the only way to watch
them is the cloud app. This custom integration gives you a **local RTSP URL** for
the live stream that your app, NVR, or Home Assistant can pull — coming up in
about a second, with the media **preferring your LAN** (it stays local when Home
Assistant is on the same network as the doorbell).

> Personal / hobby project. **Noncommercial use only.** Not affiliated with,
> endorsed by, or connected to Tuya, Smart Life, or any device manufacturer.

---

## What you get

- `camera.<name>_hd` / `camera.<name>_sd` — live camera entities.
- `sensor.<name>_rtsp_url` — the exact **LAN RTSP URL** to pull from your app / NVR
  (shown as the state; `hd` and `sd` URLs in its attributes).
- `event.<name>_datapoint` — fires on doorbell button press / datapoint changes
  (created only if you provide the doorbell's local IP during setup).

Point your app or NVR at the URL from the sensor, e.g.
`rtsp://<home-assistant-ip>:8554/tuyadb_<deviceId>_hd`.

## How it works

```
Doorbell ──WebRTC──▶ bridge ──▶ go2rtc ──RTSP/WebRTC──▶ your app / NVR / Home Assistant
          (cloud signalling,     (single hot source,
           media prefers LAN)    keyframe cache, fan-out)
```

- A small Go **bridge** (a patched build of seydx's `tuya-ipc-terminal`) speaks the
  Tuya WebRTC protocol and re-publishes the stream as RTSP. Only **signalling**
  always goes through the internet. The **media prefers your LAN** (host ICE
  candidates) and stays local when the bridge is on the same network as the
  doorbell; the Tuya TURN relay is blocked, but direct peer-to-peer over the
  internet (srflx) is allowed — so remote viewing works too. It is **not** an
  absolute LAN-only guarantee.
- **go2rtc** sits in front so the bridge only ever has one client, and fans out to
  every viewer with a cached keyframe — this is what makes it fast *and* reliable.
- A built-in **health monitor** keeps one hot session alive permanently and gently
  self-heals if the stream ever stalls, so a pull is ready at all times.

Both the bridge and go2rtc binaries (linux amd64/arm64) are bundled — if your Home
Assistant already ships go2rtc (2024.11+), that one is used automatically; otherwise
the bundled one is started on a private port.

## Requirements

- Home Assistant 2025.3 or newer, with `ffmpeg` (standard on HA OS/Supervised).
- Linux host, `amd64` or `arm64` (HA OS, Supervised, or Container).
- A Tuya / Smart Life **account** that the doorbell is paired to, and the doorbell
  on the same LAN as Home Assistant.

## Install (HACS)

1. HACS → **Custom repositories** → add `https://github.com/nihatds100/DoorbellTuya`
   as an **Integration**.
2. Install **Tuya Doorbell RTSP**, then restart Home Assistant.
3. **Settings → Devices & Services → Add Integration → Tuya Doorbell RTSP**.
4. Enter your Tuya / Smart Life **email, password, phone country code** (e.g. `1`,
   `44`, `49`…) and **server region**. Optionally enter the doorbell's **local IP**
   (e.g. `192.168.1.50`) to enable button / datapoint events. Then pick your doorbell.

The device id and local key are read **automatically** from your account during
setup — no manual key extraction, and **no Tuya IoT developer API key/secret** is
required (this uses the Smart Life app login, not the developer platform). The
doorbell's local IP is asked separately because Tuya LAN auto-discovery does not
work from inside a container; leave it empty to skip button/datapoint events.

## A note on credentials & security

You enter your own Tuya / Smart Life account details, and **you are solely
responsible** for entering, storing, and protecting them. They are used only to
talk to Tuya's own servers for stream signalling and are kept in Home Assistant's
config. This repository contains **no credentials of any kind**. Treat your Home
Assistant instance and its config as sensitive.

## Credits & attribution

This project stands entirely on the work of others — thank you:

- **[seydx/tuya-ipc-terminal](https://github.com/seydx/tuya-ipc-terminal)** (MIT) —
  the Tuya WebRTC ⭢ RTSP bridge. `bridge-src/` and the `tuya-ipc-terminal-*`
  binaries are a patched build of it (LAN-preferred media, password login, keyframe
  requests, clean teardown, stuck-session auto-recovery).
- **[AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc)** (MIT) — the restreamer
  used for reliable fan-out; shipped unmodified as `bin/go2rtc-linux-*`.
- **[jasonacox/tinytuya](https://github.com/jasonacox/tinytuya)** (MIT) — local
  datapoint (button-press) monitoring.
- **[Home Assistant](https://www.home-assistant.io/)** and its `stream` / `ffmpeg`
  components.

## License

- This project's own code: **PolyForm Noncommercial License 1.0.0** (noncommercial /
  personal use only) — see [`LICENSE`](LICENSE).
- Bundled third-party components keep their original **MIT** licenses (see `LICENSE`
  and `bridge-src/LICENSE`).

## Disclaimer

Provided "as is", without warranty of any kind. Reverse-engineered, unofficial, and
dependent on Tuya's cloud for signalling — it may break if Tuya changes their
protocol. Use at your own risk.
