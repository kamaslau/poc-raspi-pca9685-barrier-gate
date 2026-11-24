package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/googolgl/go-i2c"
	"github.com/googolgl/go-pca9685"
)

const (
	caFile = "ca.crt" // Path to the MQTT server CA certificate
)

const (
	cmdTopic   = "parking/gate/command"  // open / close
	stateTopic = "parking/gate/state"    // online / offline
	gateTopic  = "parking/gate/position" // open / close (actual barrier position)
)

var (
	broker   string
	clientId string
	username string
	password string
)

func main() {
	// Load .env.local if present; existing environment variables take precedence.
	if err := godotenv.Load(".env.local"); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to load .env.local", "err", err)
	}

	broker = os.Getenv("MQTT_BROKER")
	username = os.Getenv("MQTT_USER")
	password = os.Getenv("MQTT_PASS")
	clientId = os.Getenv("MQTT_CLIENT_ID")
	if clientId == "" {
		clientId = generateClientID()
	}

	// Initialize slog: default Info level, output to stderr; adjustable via LOG_LEVEL.
	level := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			level = slog.LevelInfo
		}
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	var symptoms []string

	// 1. Open the I2C bus (typically /dev/i2c-1 on Raspberry Pi 5).
	bus, err := i2c.New(pca9685.Address, "/dev/i2c-1")
	if err != nil {
		slog.Error("failed to open I2C bus", "err", err)
		symptoms = append(symptoms, "I2C bus open failed")
		bus = nil
	} else {
		defer bus.Close()
	}

	// 2. Initialize the PCA9685.
	var pca *pca9685.PCA9685
	if bus != nil {
		pca, err = pca9685.New(bus, nil)
		if err != nil {
			slog.Error("failed to initialize PCA9685", "err", err)
			symptoms = append(symptoms, "PCA9685 init failed")
			pca = nil
		}
	}

	// 3. Create the servo and lower the barrier.
	var gateServo *pca9685.Servo
	if pca != nil {
		gateServo = pca.ServoNew(0, nil)
		if err := gateServo.Angle(0); err != nil {
			slog.Error("failed to lower the barrier initially", "err", err)
			symptoms = append(symptoms, "initial barrier lowering failed")
			gateServo = nil
		}
	}
	if len(symptoms) == 0 {
		slog.Info("system ready, barrier lowered")
	}

	// 4. Connect to MQTT.
	tlsConfig, err := loadTLSConfig(caFile)
	if err != nil {
		slog.Error("failed to load CA certificate", "err", err)
		os.Exit(1)
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientId)
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetTLSConfig(tlsConfig)
	// Let the broker publish the offline state if the client drops unexpectedly.
	opts.SetWill(stateTopic, "offline", 1, true)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		slog.Error("connection failed", "err", token.Error())
		os.Exit(1)
	}
	slog.Info("MQTT connection established", "broker", broker, "client_id", clientId)

	// 5. Publish the online state (including diagnostics).
	initStatus := "online"
	if len(symptoms) > 0 {
		initStatus += " " + strings.Join(symptoms, ";")
	}
	if token := client.Publish(stateTopic, 1, true, initStatus); token.Wait() && token.Error() != nil {
		slog.Warn("failed to publish online state", "err", token.Error())
	}

	// 6. On hardware failure: report diagnostics, go offline, and exit.
	if len(symptoms) > 0 {
		if token := client.Publish(stateTopic, 1, true, "offline"); token.Wait() && token.Error() != nil {
			slog.Error("failed to publish offline state", "err", token.Error())
		} else {
			slog.Info("reported diagnostics and went offline", "symptoms", symptoms)
		}
		client.Disconnect(250)
		os.Exit(1)
	}

	// 7. Subscribe to commands.
	if token := client.Subscribe(cmdTopic, 1, func(c mqtt.Client, m mqtt.Message) {
		cmd := string(m.Payload())
		slog.Info("received command", "cmd", cmd)

		switch cmd {
		case "open":
			if err := gateServo.Angle(90); err != nil {
				slog.Error("failed to raise the barrier", "err", err)
				return
			}
			slog.Info("barrier raised")
			go publishPosition(c, gateTopic, "open")
		case "close":
			if err := gateServo.Angle(0); err != nil {
				slog.Error("failed to lower the barrier", "err", err)
				return
			}
			slog.Info("barrier lowered")
			go publishPosition(c, gateTopic, "close")
		default:
			slog.Warn("unknown command", "cmd", cmd)
		}
	}); token.Wait() && token.Error() != nil {
		slog.Error("subscription failed", "err", token.Error())
		os.Exit(1)
	}

	// 8. Wait for an exit signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("received exit signal, shutting down...")

	// Publish the offline state proactively (retained, QoS 1).
	if token := client.Publish(stateTopic, 1, true, "offline"); token.Wait() && token.Error() != nil {
		slog.Error("failed to publish offline state", "err", token.Error())
	} else {
		slog.Info("published offline state: offline")
	}

	client.Disconnect(250)
}

// publishPosition publishes the barrier position asynchronously to avoid blocking the MQTT callback goroutine.
func publishPosition(c mqtt.Client, topic, payload string) {
	if t := c.Publish(topic, 1, true, payload); t.Wait() && t.Error() != nil {
		slog.Error("failed to publish position state", "topic", topic, "err", t.Error())
	}
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

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
