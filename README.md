# poc-raspi-pca9685-barrier-gate

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
![Repository size](https://img.shields.io/github/repo-size/kamaslau/poc-raspi-pca9685-barrier-gate?color=56BEB8)

A barrier gate that opens on an MQTT command and reports whether it can actually move. Raspberry Pi, one PCA9685 board, one hobby servo, TLS only.

A proof of concept, not a product: it answers one question — can a gate that is already installed be driven from the software the site already runs — before anyone quotes a controller.

## Hardware

| Part | Connection | Note |
|---|---|---|
| Raspberry Pi 4 / 5 | 3.3 V (physical pin 1) → PCA9685 `VCC`; GPIO 2 / SDA (pin 3) → `SDA`; GPIO 3 / SCL (pin 5) → `SCL`; GND (for example pin 6) → `GND` | Enable I2C with `sudo raspi-config` → Interface → I2C. `VCC` powers the PCA9685 logic; use the Pi's 3.3 V, not the MB102 5 V output. |
| PCA9685 | I2C address `0x40` on `/dev/i2c-1` | `VCC` is logic power and is **not** the servo supply input `V+`. |
| MB102 / servo supply | regulated 5 V → PCA9685 `V+`; supply GND → PCA9685 `GND` and Pi GND | **Common ground is required.** Use a supply rated for the servo's stall current; do not connect its 5 V output to the Pi's 3.3 V pin. |
| Servo | PCA9685 channel 0 | An SG90, positional (not the 360° continuous variant). Connect to the channel's signal, `V+`, and `GND` pins. |

The controller runs at 50 Hz, the rate a hobby servo expects. No other wiring is assumed: the gate mechanism is whatever the site already has, and the servo horn is the only point of contact.

### Wiring

```text
┌────────────────────┐                             ┌───────────────────────┐
│    Raspberry Pi    │                             │        PCA9685        │
│                    │                             │   0x40 · /dev/i2c-1   │
│ 3V3        (1)  ●──┼─────────────────────────────┼──● VCC                │
│ GPIO2/SDA  (3)  ●──┼─────────────────────────────┼──● SDA                │
│ GPIO3/SCL  (5)  ●──┼─────────────────────────────┼──● SCL                │
│ GND        (6)  ●──┼─────────────────────────────┼──● GND                │
│                    │                             │                       │
└────────────────────┘                             │                       │
                                                   │                       │
                       ┌──────────────────────┐    │                       │
                       │        MB102         │    │                       │
                       │   5V · 2A or more    │    │                       │
                       │  V+  ●───────────────┼────┼──● V+  (servo rail)   │
                       │  GND ●───────────────┼────┼──● GND (servo return) │
                       └──────────────────────┘    │                       │
                                                   │    S    +    −        │
                                                   └────┬────┬────┬────────┘
                                                        │    │    │
                                                        │    │    │
                                                     ┌──┴────┴────┴──┐
                                                     │     SG90      │
                                                     │  S    +    −  │
                                                     └───────────────┘
```

The numbers in brackets are physical pins on the Pi header, not GPIO numbers. Two supplies meet here and are not interchangeable: the Pi's **3.3 V** powers the PCA9685 logic (`VCC`) and the I²C bus, and the **5 V** supply powers the servo through `V+`. Never take servo power from the Pi's own 5 V pins — a stalling servo pulls several hundred milliamps from a rail the Pi shares with its USB ports.

Ground is one net, and every part reaches it. Without that shared reference the pulse means nothing to the servo: it buzzes and will not move.

`2A or more` is not decoration. An MB102-class breadboard supply is rated around 700 mA, and a servo that stalls on it draws more than that, so the module's protection trips: the LED lights at rest and goes out under a command. The gate then goes silent and free-turning with nothing wrong in the log. If you see that, the supply is the fault, not the servo.

### Troubleshooting `remote I/O error`

`/dev/i2c-1` existing only confirms that the Pi's I2C adapter is available; it does not confirm that a PCA9685 is powered or responding. The application expects a device at address `0x40`. If initialization reports `remote I/O error`:

1. Stop the application and verify the wiring above, especially **Pi 3.3 V → PCA9685 `VCC`**. Supplying 5 V to `V+` does not power the PCA9685 logic.
2. Check that SDA goes to SDA, SCL goes to SCL (not “SDL”), and the Pi, PCA9685, and MB102 grounds are connected together.
3. Check that the PCA9685 address jumpers leave it at `0x40` (the board's usual default). With `i2c-tools` installed, run `sudo i2cdetect -y 1`; `40` should appear. `70` will appear too — that is the chip's all-call address, not a second device, and no re-jumpering is needed for it. A missing `40` means the board is not acknowledging on this bus—recheck power, wiring, and address before running the application again.

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

The value is in the servo's actuation range — 0 to 180 — and **not** in degrees of barrier travel. That range is mapped onto a 1000–2000 µs pulse band: `0` is 1000 µs, `180` is 2000 µs, and the default `90` is 1500 µs. Both ends of the band sit inside an SG90's mechanical travel, so no commanded position parks the horn against its stop — that stall is what strips the nylon gear train in the first place.

The band spans roughly 90° of horn travel, which is the swing a barrier arm wants. Set the value by watching the barrier, not by reading the number: the linkage between horn and arm decides what a given angle looks like at the gate. Widening the pulses beyond 1000–2000 µs (`servoMinPulse` / `servoMaxPulse` in `main.go`) buys travel by grinding the stops, so tune `GATE_OPEN_ANGLE` first.

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
#    Move .env.local aside first — godotenv loads that file, and a value in it
#    counts as configuration.
mv .env.local .env.local.bak
env -u MQTT_BROKER -u MQTT_USER -u MQTT_PASS go run main.go; echo "exit=$?"
# level=ERROR msg="missing required configuration" vars="[MQTT_BROKER MQTT_USER MQTT_PASS]" ...
# exit=2
mv .env.local.bak .env.local

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

**A retained position, not a retained command ack.** The command topic is not retained: replaying `open` to a client that reconnects would move a gate nobody asked to move. The retain flag belongs to the publisher, though, so a command that *arrives* retained is refused on receipt — one stray retained `open` would otherwise re-open the gate after every reconnect. The position is retained instead, and republished when start-up parks the barrier, so that topic never describes a position the gate has already left.

**Last will plus a republish on connect.** The will covers "the process died" — the broker says `offline` on our behalf. It cannot cover "the process came back", so `OnConnect` republishes `online` and re-subscribes on every connection. paho does not restore subscriptions after a reconnect by default (`ResumeSubs` is false, the session is clean), which is the failure this exists to prevent.

**Connect before giving up on hardware.** A gate with a loose I2C wire still exits, but only after publishing why. The person debugging it is looking at MQTT, not at `journalctl` on a Pi inside a cabinet.

**Hardware failures are not retried in-process.** One start-up attempt, one report, exit 1. Retrying a bus that is not there produces a log full of identical lines and no useful signal.

## License

AGPL-3.0 — see [LICENSE](./LICENSE).

---

Built by [Kamas Lau](https://www.kamaslau.com/works?utm_source=github&utm_medium=repo-readme&utm_campaign=always-on&utm_content=poc) — I do this for foreign companies operating in China, on their own infrastructure.
