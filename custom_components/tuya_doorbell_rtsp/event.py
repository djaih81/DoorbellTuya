"""Event entities: fire on local datapoint pushes (button press, motion, etc.)."""
from __future__ import annotations

from homeassistant.components.event import EventDeviceClass, EventEntity
from homeassistant.components.tuya.const import TUYA_HA_SIGNAL_UPDATE_ENTITY
from homeassistant.core import callback
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.dispatcher import async_dispatcher_connect

from .const import CONF_CAMERAS, CONF_DEVICE_IP, DOMAIN
from .doorbell import DoorbellMessages


def signal(entry_id, device_id):
    return f"{DOMAIN}_{entry_id}_{device_id}_dp"


async def async_setup_entry(hass, entry, async_add_entities):
    store = hass.data[DOMAIN][entry.entry_id]
    details = store["details"]
    entities = []
    cameras = {c["deviceId"]: c for c in store["bridge"].list_cameras()}
    for device_id in entry.data.get(CONF_CAMERAS, []):
        if camera := cameras.get(device_id):
            entities.append(TuyaCloudDoorbellEvent(camera))
    if entry.data.get(CONF_DEVICE_IP):
        for dev_id in entry.data.get(CONF_CAMERAS, []):
            detail = details.get(dev_id)
            if not detail:
                continue
            entities.append(TuyaDPEvent(entry.entry_id, detail))
    async_add_entities(entities)


class TuyaCloudDoorbellEvent(EventEntity):
    """Use HA's existing Tuya updates; do not create a video or device socket."""

    _attr_has_entity_name = True
    _attr_name = "Sonnette"
    _attr_event_types = ["ring"]
    _attr_device_class = EventDeviceClass.DOORBELL
    _attr_should_poll = False

    def __init__(self, camera):
        self._device_id = camera["deviceId"]
        self._attr_unique_id = f"{self._device_id}_cloud_ring"
        self._messages = DoorbellMessages()
        self._baselined = False
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, self._device_id)},
            name=camera.get("deviceName", "Tuya Doorbell"),
            manufacturer="Tuya", model=camera.get("productId"),
        )

    def _device(self):
        for entry in self.hass.config_entries.async_entries("tuya"):
            manager = getattr(getattr(entry, "runtime_data", None), "manager", None)
            if manager is not None and (device := manager.device_map.get(self._device_id)):
                return device
        return None

    @property
    def available(self):
        device = self._device()
        return device is not None and device.online

    async def async_added_to_hass(self):
        if device := self._device():
            self._messages.observe(device.status.get("initiative_message"), baseline=True)
            self._baselined = True
        self.async_on_remove(async_dispatcher_connect(
            self.hass, f"{TUYA_HA_SIGNAL_UPDATE_ENTITY}_{self._device_id}", self._update_cloud,
        ))

    @callback
    def _update_cloud(self, updated_status_properties=None, dp_timestamps=None):
        device = self._device()
        if device is None:
            return
        value = device.status.get("initiative_message")
        if not self._baselined:
            self._messages.observe(value, baseline=True)
            self._baselined = True
        elif updated_status_properties and "initiative_message" in updated_status_properties:
            if self._messages.observe(value):
                self._trigger_event("ring", {"source": "tuya_cloud"})
        self.async_write_ha_state()


class TuyaDPEvent(EventEntity):
    """Fires whenever the device pushes a datapoint update (async event)."""

    _attr_has_entity_name = True
    _attr_name = "Datapoint"
    _attr_event_types = ["dp"]

    def __init__(self, entry_id, detail):
        self._entry_id = entry_id
        self._device_id = detail["deviceId"]
        self._attr_unique_id = f"{self._device_id}_dp_event"
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, self._device_id)},
            name=detail.get("deviceName", "Tuya Doorbell"),
            manufacturer="Tuya",
            model=detail.get("productId"),
        )

    async def async_added_to_hass(self):
        self.async_on_remove(
            async_dispatcher_connect(
                self.hass, signal(self._entry_id, self._device_id), self._handle
            )
        )

    def _handle(self, dp, value):
        # dispatcher may run us in an executor thread; hop to the loop.
        self.hass.loop.call_soon_threadsafe(self._update, str(dp), value)

    @callback
    def _update(self, dp, value):
        self._trigger_event("dp", {"dp": dp, "value": value})
        self.async_write_ha_state()
