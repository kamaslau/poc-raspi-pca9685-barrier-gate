package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/googolgl/go-i2c"
	"github.com/googolgl/go-pca9685"
)

const (
	caFile = "ca.crt" // Path to the MQTT server CA certificate
)

const (
	cmdTopic      = "parking/gate/command"  // open / close
	stateTopic    = "parking/gate/state"    // online / offline
	positionTopic = "parking/gate/position" // open / close (actual barrier position)
	healthTopic   = "parking/gate/health"   // retained JSON diagnostics
)

// Exit codes separate the failure classes a supervisor has to tell apart: a
// restart policy that fits a missing cable does not fit a refused password.
const (
	exitHardware = 1 // I2C bus, PCA9685 or servo could not be brought up
	exitConfig   = 2 // missing or invalid configuration
	exitBroker   = 3 // broker unreachable, or authentication refused
)

const (
	stateOnline  = "online"
	stateOffline = "offline"

	positionOpen   = "open"
	positionClosed = "close"

	defaultOpenAngle = 90
)

// The actuator is an SG90: 180 units of actuation, driven over a 1000-2000 us
// pulse band. The library defaults (750-2250 us over 135 units) are wrong for
// it in both directions - the range is not 135, and 750 us sits past the low
// mechanical stop, so parking the gate at angle 0 leaves the servo stalled
// against the case. The gear train is nylon, and that is how it strips.
//
// Units, not degrees: the band spans roughly 90 degrees of horn travel, which
// is what a barrier arm wants. Wider pulses than these buy travel at the cost
// of grinding the stops, so the travel is tuned with GATE_OPEN_ANGLE instead.
const (
	servoSweep    = 180  // actuation range, in the library's angle units
	servoMinPulse = 1000 // us at angle 0
	servoMaxPulse = 2000 // us at servoSweep
)

// healthReport is the retained payload on healthTopic. It carries what the
// start-up sequence managed to bring up, so a subscriber never has to read the
// process log to find out why a gate did not move.
type healthReport struct {
	OK       bool     `json:"ok"`
	Symptoms []string `json:"symptoms,omitempty"`
	ClientID string   `json:"client_id"`
	At       string   `json:"at"`
}

func main() {
	// Load .env.local if present; existing environment variables take precedence.
	if err := godotenv.Load(".env.local"); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to load .env.local", "err", err)
	}
	initLogging()

	// Broker credentials are required. There is no fallback: a default that
	// works is a credential in a public repository.
	var missing []string
	for _, key := range []string{"MQTT_BROKER", "MQTT_USER", "MQTT_PASS"} {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		slog.Error("missing required configuration", "vars", missing, "hint", "copy .env.example to .env.local and fill it in")
		os.Exit(exitConfig)
	}
	broker := os.Getenv("MQTT_BROKER")
	username := os.Getenv("MQTT_USER")
	password := os.Getenv("MQTT_PASS")

	clientID := os.Getenv("MQTT_CLIENT_ID")
	if clientID == "" {
		clientID = generateClientID()
	}

	openAngle, err := openAngleFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(exitConfig)
	}

	// 1-3. Bring the hardware up. A failure here is not fatal yet: the symptoms
	// are reported over MQTT before the process exits, so the reason is visible
	// from the same place the commands arrive.
	bus, servo, symptoms := initHardware()
	if bus != nil {
		defer bus.Close()
	}
	hardwareOK := len(symptoms) == 0
	if hardwareOK {
		slog.Info("system ready, barrier lowered", "open_angle", openAngle)
	}

	// 4. Connect to MQTT.
	tlsConfig, err := loadTLSConfig(caFile)
	if err != nil {
		slog.Error("failed to load CA certificate", "file", caFile, "err", err)
		os.Exit(exitConfig)
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetTLSConfig(tlsConfig)
	// Let the broker publish the offline state if the client drops unexpectedly.
	opts.SetWill(stateTopic, stateOffline, 1, true)
	// Every connection — the first one and every reconnect — re-publishes the
	// state and re-subscribes. paho does not restore subscriptions after a
	// reconnect unless the broker keeps the session, and a device that loses its
	// subscription on a brief network drop is a device someone has to walk up to
	// and restart.
	//
	// The handler runs in its own goroutine, so Connect() returning only means the
	// broker sent CONNACK — not that our state and health are on the wire yet.
	// firstConnect closes once those publishes have been acknowledged, which is
	// what the hardware-failure path below waits for before disconnecting.
	firstConnect := make(chan struct{})
	var firstConnectOnce sync.Once
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		onConnect(c, hardwareOK, symptoms, clientID, servo, openAngle)
		firstConnectOnce.Do(func() { close(firstConnect) })
	})

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		slog.Error("connection failed", "broker", broker, "err", token.Error())
		os.Exit(exitBroker)
	}
	slog.Info("MQTT connection established", "broker", broker, "client_id", clientID)

	select {
	case <-firstConnect:
	case <-time.After(10 * time.Second):
		slog.Warn("state and health were not published within 10s of connecting")
	}

	// 5. A gate that cannot move is not a gate. The diagnostics are already on
	// healthTopic and state reads offline; leave the process so a supervisor
	// restarts it.
	if !hardwareOK {
		slog.Error("hardware not ready, diagnostics published", "symptoms", symptoms)
		client.Disconnect(250)
		os.Exit(exitHardware)
	}

	// 6. Hardware start-up parked the barrier, so the retained position is
	// `close` whatever a previous run left on the topic. Without this, a
	// restart that lowers the gate leaves the topic claiming `open`, and the
	// position is the one retained value a consumer is meant to trust.
	if publishRetained(client, positionTopic, positionClosed) {
		slog.Info("published position", "topic", positionTopic, "position", positionClosed)
	}

	// 7. Wait for an exit signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("received exit signal, shutting down...")

	// Publish the offline state proactively (retained, QoS 1).
	if publishRetained(client, stateTopic, stateOffline) {
		slog.Info("published offline state", "topic", stateTopic)
	}

	client.Disconnect(250)
}

// onConnect runs on the first connection and on every reconnect. It is the only
// place that publishes the online state and subscribes, so there is exactly one
// code path that can leave the device half-connected.
func onConnect(c mqtt.Client, hardwareOK bool, symptoms []string, clientID string, servo *pca9685.Servo, openAngle int) {
	state := stateOnline
	if !hardwareOK {
		state = stateOffline
	}
	publishRetained(c, stateTopic, state)
	publishHealth(c, hardwareOK, symptoms, clientID)

	if !hardwareOK {
		return
	}

	if token := c.Subscribe(cmdTopic, 1, commandHandler(c, servo, openAngle)); token.Wait() && token.Error() != nil {
		slog.Error("subscription failed", "topic", cmdTopic, "err", token.Error())
		return
	}
	slog.Info("subscribed to commands", "topic", cmdTopic)
}

// commandHandler reacts to open / close. The position is published from a
// separate goroutine so a slow broker cannot stall the MQTT callback.
func commandHandler(c mqtt.Client, servo *pca9685.Servo, openAngle int) mqtt.MessageHandler {
	return func(_ mqtt.Client, m mqtt.Message) {
		cmd, ok := parseCommand(m.Payload(), m.Retained())
		if !ok {
			return
		}
		slog.Info("received command", "cmd", cmd)

		switch cmd {
		case positionOpen:
			if err := servo.Angle(openAngle); err != nil {
				slog.Error("failed to raise the barrier", "angle", openAngle, "err", err)
				return
			}
			slog.Info("barrier raised", "angle", openAngle)
			go publishRetained(c, positionTopic, positionOpen)
		case positionClosed:
			if err := servo.Angle(0); err != nil {
				slog.Error("failed to lower the barrier", "err", err)
				return
			}
			slog.Info("barrier lowered")
			go publishRetained(c, positionTopic, positionClosed)
		}
	}
}

// parseCommand validates a command payload before anything moves.
//
// A retained command is a replay of something that already ran: the broker hands
// it to every subscriber that arrives later, including this device after every
// reconnect. Acting on it would move a gate nobody asked to move, and the retain
// flag belongs to the publisher, so refusing it here is the only place this can
// be enforced.
func parseCommand(payload []byte, retained bool) (string, bool) {
	cmd := strings.TrimSpace(string(payload))
	if retained {
		slog.Warn("ignoring retained command", "cmd", cmd, "hint", "publish without the retain flag, and clear the retained message")
		return "", false
	}

	switch cmd {
	case positionOpen, positionClosed:
		return cmd, true
	default:
		slog.Warn("unknown command", "cmd", cmd)
		return "", false
	}
}

// initHardware opens the bus, initialises the controller and parks the barrier.
// Failures are collected rather than returned: the caller still needs a client to
// report them. The bus is returned so the caller keeps it open for the lifetime
// of the process — closing it here would leave the servo unreachable.
func initHardware() (*i2c.Options, *pca9685.Servo, []string) {
	// 1. Open the I2C bus (typically /dev/i2c-1 on Raspberry Pi 4/5).
	bus, err := i2c.New(pca9685.Address, "/dev/i2c-1")
	if err != nil {
		slog.Error("failed to open I2C bus", "device", "/dev/i2c-1", "err", err)
		return nil, nil, []string{"i2c bus open failed"}
	}

	// 2. Initialise the PCA9685. The library sets 50 Hz PWM in New(), which is
	// what a hobby servo expects.
	pca, err := pca9685.New(bus, nil)
	if err != nil {
		slog.Error("failed to initialize PCA9685", "address", "0x40", "err", err,
			"hint", "check PCA9685 VCC (3.3V), SDA/SCL wiring, common ground, and address jumpers")
		return bus, nil, []string{"pca9685 init failed"}
	}

	// 3. Create the servo and lower the barrier. The pulse band is explicit so
	// the parked position is angle 0 -> servoMinPulse, not a library default
	// that would hold the horn against its stop.
	gateServo := pca.ServoNew(0, &pca9685.ServOptions{
		AcRange:  servoSweep,
		MinPulse: servoMinPulse,
		MaxPulse: servoMaxPulse,
	})
	if err := gateServo.Angle(0); err != nil {
		slog.Error("failed to lower the barrier initially", "err", err)
		return bus, nil, []string{"initial barrier lowering failed"}
	}

	return bus, gateServo, nil
}

// openAngleFromEnv reads GATE_OPEN_ANGLE. The value is in the servo's actuation
// range (0-180 for the SG90), not degrees of barrier travel: the range is mapped
// onto the 1000-2000 us pulse band, so 90 is 1500 us.
func openAngleFromEnv() (int, error) {
	raw := os.Getenv("GATE_OPEN_ANGLE")
	if raw == "" {
		return defaultOpenAngle, nil
	}

	angle, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("GATE_OPEN_ANGLE must be an integer, got %q", raw)
	}
	if angle < 0 || angle > servoSweep {
		return 0, fmt.Errorf("GATE_OPEN_ANGLE must be between 0 and %d, got %d", servoSweep, angle)
	}

	return angle, nil
}

// publishRetained publishes at QoS 1 with the retained flag and reports whether
// the broker acknowledged it.
func publishRetained(c mqtt.Client, topic, payload string) bool {
	token := c.Publish(topic, 1, true, payload)
	if token.Wait() && token.Error() != nil {
		slog.Error("failed to publish", "topic", topic, "err", token.Error())
		return false
	}
	return true
}

// publishHealth publishes the retained diagnostics report.
func publishHealth(c mqtt.Client, ok bool, symptoms []string, clientID string) {
	payload, err := json.Marshal(healthReport{
		OK:       ok,
		Symptoms: symptoms,
		ClientID: clientID,
		At:       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		slog.Error("failed to encode health report", "err", err)
		return
	}
	publishRetained(c, healthTopic, string(payload))
}

// initLogging sets up slog: Info level by default, output to stderr, adjustable
// via LOG_LEVEL (debug / info / warn / error).
func initLogging() {
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			level = slog.LevelInfo
		}
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

func loadTLSConfig(caFile string) (*tls.Config, error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	certpool := x509.NewCertPool()
	if !certpool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("failed to parse CA certificate: %s", caFile)
	}

	return &tls.Config{
		RootCAs: certpool, // InsecureSkipVerify defaults to false
	}, nil
}

// generateClientID returns "device-" followed by 8 random hex characters,
// similar to a git short commit hash.
func generateClientID() string {
	b := make([]byte, 4) // 4 bytes -> 8 hex chars
	if _, err := rand.Read(b); err != nil {
		// Fallback to a timestamp-based suffix if crypto/rand fails.
		return fmt.Sprintf("device-%08x", os.Getpid())
	}
	return "device-" + hex.EncodeToString(b)
}
