"""Exercise ownership without starting HA, ffmpeg, or a real camera."""
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import AsyncMock

for name in ("homeassistant", "homeassistant.helpers", "homeassistant.helpers.aiohttp_client", "aiohttp"):
    sys.modules.setdefault(name, types.ModuleType(name))
sys.modules["aiohttp"].ClientTimeout = lambda **kw: kw
sys.modules["homeassistant.helpers.aiohttp_client"].async_get_clientsession = lambda hass: hass.session
path = Path(__file__).parents[1] / "custom_components/tuya_doorbell_rtsp/go2rtc.py"
spec = importlib.util.spec_from_file_location("tested_go2rtc", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class Response:
    def __init__(self, status=200): self.status = status
    async def __aenter__(self): return self
    async def __aexit__(self, *args): pass

class Session:
    def __init__(self): self.deleted, self.puts = [], []
    def delete(self, url, **kw): self.deleted.append(url); return Response()
    def put(self, url, **kw): self.puts.append(url); return Response()

class CleanupTest(unittest.IsolatedAsyncioTestCase):
    def manager(self):
        hass = types.SimpleNamespace(config=types.SimpleNamespace(path=lambda name: name), session=Session())
        mgr = module.Go2rtc(hass, Path("unused"))
        mgr._api, mgr._rtsp_port = "http://127.0.0.1:1984", 8554
        return mgr, hass.session

    async def test_shared_go2rtc_removes_own_streams_only(self):
        mgr, session = self.manager()
        self.assertTrue(await mgr.async_register(28554, "/TestCamera", "test-device"))
        await mgr.async_stop()
        self.assertEqual(session.deleted, ["http://127.0.0.1:1984/api/streams?name=tuyadb_test-device_hd"])
        self.assertEqual(len(session.puts), 1)
        self.assertFalse(mgr.available)
        await mgr.async_stop()
        self.assertEqual(len(session.deleted), 1)

    async def test_private_process_is_reaped_after_kill(self):
        mgr, _ = self.manager()
        proc = types.SimpleNamespace(returncode=None, terminate=lambda: None, kill=lambda: None)
        proc.wait = AsyncMock(side_effect=[TimeoutError(), 0])
        mgr._proc = proc
        await mgr.async_stop()
        self.assertEqual(proc.wait.await_count, 2)
        self.assertIsNone(mgr._proc)

if __name__ == "__main__": unittest.main()
