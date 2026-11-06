import base64
import importlib.util
import json
from pathlib import Path
import unittest

path = Path(__file__).parents[1] / "custom_components/tuya_doorbell_rtsp/doorbell.py"
spec = importlib.util.spec_from_file_location("doorbell_messages", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def message(cmd="ipc_doorbell", image="synthetic-image"):
    return base64.b64encode(json.dumps({"cmd": cmd, "type": "image", "files": [image]}).encode()).decode()


class DoorbellMessageTest(unittest.TestCase):
    def test_cached_message_never_rings_on_startup(self):
        detector = module.DoorbellMessages()
        cached = message()
        self.assertFalse(detector.observe(cached, baseline=True))
        self.assertFalse(detector.observe(cached))
        self.assertTrue(detector.observe(message(image="new-image")))

    def test_duplicate_cloud_delivery_is_ignored(self):
        detector = module.DoorbellMessages()
        self.assertTrue(detector.observe(message()))
        self.assertFalse(detector.observe(message()))

    def test_movement_is_not_a_doorbell_ring(self):
        self.assertFalse(module.DoorbellMessages().observe(message(cmd="ipc_motion")))

    def test_invalid_payloads_do_not_ring(self):
        for payload in [None, 1, "", "not base64", "x"*65537, base64.b64encode(b"[]").decode(), base64.b64encode(b"not json").decode()]:
            self.assertFalse(module.DoorbellMessages().observe(payload))

    def test_missing_base64_padding_is_supported(self):
        self.assertTrue(module.DoorbellMessages().observe(message(image="x").rstrip("=")))

if __name__ == "__main__": unittest.main()
