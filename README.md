# poc-raspi-pca9685-barrier-gate

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
![Repository size](https://img.shields.io/github/repo-size/kamaslau/poc-raspi-pca9685-barrier-gate?color=56BEB8)

A barrier gate that opens on an MQTT command and reports whether it can actually move. Raspberry Pi, one PCA9685 board, one hobby servo, TLS only.

A proof of concept, not a product: it answers one question — can a gate that is already installed be driven from the software the site already runs — before anyone quotes a controller.

## Hardware

| Part | Connection | Note |
|---|---|---|
| Raspberry Pi 4 / 5 | I2C bus 1: GPIO 2 (SDA), GPIO 3 (SCL) | enable with `sudo raspi-config` → Interface → I2C, then `ls /dev/i2c-1` |
| PCA9685 | address `0x40` on `/dev/i2c-1` | logic side runs at 3.3 V, which the Pi provides |
| Servo | PCA9685 channel 0 | **separate 5 V supply, common ground with the Pi** — servo stall current will brown out the Pi's 5 V rail otherwise |

The controller runs at 50 Hz, the rate a hobby servo expects. No other wiring is assumed: the gate mechanism is whatever the site already has, and the servo horn is the only point of contact.

## Topics

All four run at QoS 1. `state`, `position` and `health` are retained, so a subscriber that arrives late still learns the current state without waiting for the next event.

| Topic | Direction | Retained | Payload |
|---|---|---|---|
| `parking/gate/command` | subscribe | no | `open` or `close` |
| `parking/gate/state` | publish | yes | `online` or `offline` |
| `parking/gate/position` | publish | yes | `open` or `close` — what the barrier last did |
| `parking/gate/health` | publish | yes | JSON, see below |

`state` carries two words and nothing else, so a rule can compare it. Diagnostics do not belong in a value someone else has to parse, so they go to `health`:

```json
{"ok":false,"symptoms":["i2c bus open failed"],"client_id":"device-ef209726","at":"2026-11-24T03:04:14Z"}
```

`symptoms` is omitted when the hardware came up. The names match the start-up steps: `i2c bus open failed`, `pca9685 init failed`, `initial barrier lowering failed`.

## Usage

`MQTT_BROKER`, `MQTT_USER` and `MQTT_PASS` are required. There is no built-in default and the process refuses to start without them — a default that works is a credential sitting in a public repository.

```shell
cp .env.example .env.local   # fill in MQTT_BROKER / MQTT_USER / MQTT_PASS
                             # MQTT_CLIENT_ID is optional; generated as device-<8 hex> if unset
cp /path/to/ca.crt .         # CA that signed the broker certificate
go run main.go
```

The broker URI needs the `ssl://` scheme (`ssl://host:8883`); the client verifies the server against `ca.crt` and does not skip verification.

### `GATE_OPEN_ANGLE`

The value is in the servo library's actuation range — 0 to 135 with the defaults — and **not** in degrees of barrier travel. With the default 750–2250 µs pulse range, `90` is two thirds of the sweep, about 1750 µs. Set it by watching the barrier, not by reading the number: the linkage between horn and arm decides what a given angle looks like at the gate.

## Exit codes

| Code | Meaning | What a supervisor should do |
|---|---|---|
| 0 | stopped by SIGINT / SIGTERM, `offline` published | leave it stopped |
| 1 | hardware did not come up; diagnostics are on `health` | inspect the wiring, restart |
| 2 | configuration missing or invalid | fix the config, do not loop on restart |
| 3 | broker unreachable or refused the credentials | check network and credentials |

The split matters because the restart policy for a missing cable is not the restart policy for a wrong password.

## Verify

Three commands, in the order the failures actually happen.

```shell
# 1. No credentials: refuses to start rather than connecting somewhere by default.
env -u MQTT_BROKER -u MQTT_USER -u MQTT_PASS go run main.go; echo "exit=$?"
# level=ERROR msg="missing required configuration" vars="[MQTT_BROKER MQTT_USER MQTT_PASS]" ...
# exit=2

# 2. Credentials but no I2C device: connects, reports why, goes offline, exits.
go run main.go; echo "exit=$?"      # with /dev/i2c-1 absent
mosquitto_sub -h "$HOST" -p 8883 --cafile ca.crt -t 'parking/gate/#' -v
# parking/gate/state offline
# parking/gate/health {"ok":false,"symptoms":["i2c bus open failed"],...}
# exit=1

# 3. Fully wired: the gate moves and says so.
mosquitto_pub -h "$HOST" -p 8883 --cafile ca.crt -t parking/gate/command -m open
# log: level=INFO msg="barrier raised" angle=90
# retained: parking/gate/position open
```

Reconnect behaviour has no command of its own — cut the network for 30 seconds and watch `state`: the broker's last will publishes `offline` when the client drops, and the client republishes `online` and re-subscribes when it returns. Without the second half, a device that saw one network blip would answer nothing until someone power-cycled it.

Unit tests cover configuration parsing and the health payload shape: `go test ./...`.

## Why this shape

**State separated from diagnostics.** `online` / `offline` is a fact a rule can compare; `online i2c bus open failed` is a sentence. The first version put both in one payload, which left every consumer parsing free text to answer a yes/no question.

**A retained position, not a retained command ack.** The command topic is not retained: replaying `open` to a client that reconnects would move a gate nobody asked to move. The position is retained instead, so the state of the barrier is readable at any time without an ack round trip.

**Last will plus a republish on connect.** The will covers "the process died" — the broker says `offline` on our behalf. It cannot cover "the process came back", so `OnConnect` republishes `online` and re-subscribes on every connection. paho does not restore subscriptions after a reconnect by default (`ResumeSubs` is false, the session is clean), which is the failure this exists to prevent.

**Connect before giving up on hardware.** A gate with a loose I2C wire still exits, but only after publishing why. The person debugging it is looking at MQTT, not at `journalctl` on a Pi inside a cabinet.

**Hardware failures are not retried in-process.** One start-up attempt, one report, exit 1. Retrying a bus that is not there produces a log full of identical lines and no useful signal.

## License

AGPL-3.0 — see [LICENSE](./LICENSE).

---

Built by [Kamas Lau](https://www.kamaslau.com/works?utm_source=github&utm_medium=repo-readme&utm_campaign=always-on&utm_content=poc) — I do this for foreign companies operating in China, on their own infrastructure.
