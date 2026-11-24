# poc-raspi-pca9685-barrier-gate

PoC: Raspberry Pi + PCA9685 servo-controlled parking barrier gate, driven by MQTT commands over TLS. Go implementation with retained state/position topics and startup hardware diagnostics.

- Raspberry Pi 4/5
- PCA9685
- Servo wired to PCA9685 channel 0 on `/dev/i2c-1`
- MQTT server

## Usage

Place your MQTT server CA certificate as `ca.crt` in the working directory, then:

```shell
cp .env.example .env.local   # fill in MQTT_BROKER / MQTT_USER / MQTT_PASS
                             # MQTT_CLIENT_ID is optional; auto-generated if unset
go run main.go
```
