# Contributing

This is a proof of concept kept small on purpose. Bug reports and fixes are welcome; features that turn it into a product are not — that is a different repository.

## Before you start

- Go 1.27 or newer.
- The `go-i2c` dependency compiles only on Linux, because it talks to `/dev/i2c-1` directly. On macOS the build fails at the `linux/i2c-dev.h` include; use a Linux host or a container (`golang:1.27` works).
- Hardware is not required to run the tests. Only the wiring checks need a Raspberry Pi.

## Running it

```shell
cp .env.example .env.local   # MQTT_BROKER / MQTT_USER / MQTT_PASS are required
cp /path/to/ca.crt .
go run main.go
```

Without `/dev/i2c-1` the process exits with code 1 after publishing `parking/gate/health`. That is the intended behaviour, not a bug — see the exit code table in the README.

## Before opening a pull request

```shell
gofmt -l .
go vet ./...
go test ./...
```

All three must be clean. `gofmt -l` printing a filename is a failure, not a warning.

If your change touches the start-up sequence, the topics, or the exit codes, update the README in the same commit. The README is the contract; the code is one implementation of it.

## Reporting a hardware problem

Include the retained `parking/gate/health` payload and the kernel side of the I2C bus:

```shell
mosquitto_sub -h "$HOST" -p 8883 --cafile ca.crt -t parking/gate/health -C 1 -v
i2cdetect -y 1
```

`i2cdetect` showing nothing at `0x40` means the wiring, not the code.

## Commit messages

Imperative mood, one line under 72 characters, and the reason in the body when the change is not obvious:

```
fix: re-subscribe after a reconnect

paho does not restore subscriptions when the session is clean, so a gate
that lost its connection for ten seconds never received another command.
```

Do not mix formatting changes with behaviour changes in one commit.

## License

Contributions are accepted under AGPL-3.0, the license this repository already carries.
