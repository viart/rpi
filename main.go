package main

import (
	"context"
	"flag"
	"log"
	"strconv"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/spf13/viper"
	"gobot.io/x/gobot"
	"gobot.io/x/gobot/drivers/gpio"
	"gobot.io/x/gobot/drivers/i2c"
	"gobot.io/x/gobot/platforms/mqtt"
	"gobot.io/x/gobot/platforms/raspi"
)

type config struct {
	Mqtt struct {
		Host    string
		Preffix string
	}
	Bme280 struct {
		Interval time.Duration
		Address  int
	}
	Ble struct {
		Interval     time.Duration
		Duration     time.Duration
		MqttPreffix  string   `yaml:"mqtt-preffix"`
		KnownDevices []string `yaml:"known-devices"`
	}
	Pir struct {
		Pin        string
		MqttSuffix string `yaml:"mqtt-suffix"`
	}
}

var (
	cfg        config
	configFile = flag.String("config", "config", "Config file")
)

func float32bytes(value float32) []byte {
	return strconv.AppendFloat(make([]byte, 0, 6), float64(value), 'f', 1, 32)
}

func init() {
	viper.AddConfigPath(".")
	viper.SetConfigName(*configFile)

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading a config file, %s", err)
	}

	err := viper.Unmarshal(&cfg)
	if err != nil {
		log.Fatalf("Error decoding into a config struct, %v", err)
	}
}

func x(suffix string) string {
	return cfg.Mqtt.Preffix + suffix
}

func main() {
	flag.Parse()

	d, err := linux.NewDevice()
	if err != nil {
		log.Fatalf("Error initializing of a ble device : %s", err)
	}

	rpiAdaptor := raspi.NewAdaptor()
	mqttAdaptor := mqtt.NewAdaptor(cfg.Mqtt.Host, "rpi")
	mqttAdaptor.SetAutoReconnect(true)

	bme280 := i2c.NewBME280Driver(rpiAdaptor, i2c.WithAddress(cfg.Bme280.Address)) // 0x76
	pir := gpio.NewPIRMotionDriver(rpiAdaptor, cfg.Pir.Pin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	onMotionHandler := func(data interface{}) {
		mqttAdaptor.Publish(x(cfg.Pir.MqttSuffix), data.([]byte))
	}

	work := func() {
		//TODO: paho + LWT setWill + onConnect
		gobot.Every(1*time.Minute, func() {
			mqttAdaptor.Publish(cfg.Mqtt.Preffix+"heartbeat", []byte{})
		})

		gobot.Every(cfg.Ble.Interval, func() {
			ctx, cancel := context.WithTimeout(ctx, cfg.Ble.Duration)
			defer cancel()
			d.Scan(ctx, false, func(a ble.Advertisement) {
				for _, item := range cfg.Ble.KnownDevices {
					if item == a.Addr().String() {
						mqttAdaptor.Publish(cfg.Ble.MqttPreffix+item, []byte("home"))
					}
				}
			})
		})

		pir.On(gpio.MotionDetected, onMotionHandler)
		pir.On(gpio.MotionStopped, onMotionHandler)

		gobot.Every(cfg.Bme280.Interval, func() {
			if t, err := bme280.Temperature(); err == nil {
				mqttAdaptor.Publish(x("temperature"), float32bytes(t))
			}

			if p, err := bme280.Pressure(); err == nil {
				mqttAdaptor.Publish(x("pressure"), float32bytes(p/100))
			}

			if h, err := bme280.Humidity(); err == nil {
				mqttAdaptor.Publish(x("humidity"), float32bytes(h))
			}
		})

		// mqttAdaptor.On("hello", func(msg mqtt.Message) { fmt.Println(msg) })
	}

	robot := gobot.NewRobot("bot",
		[]gobot.Connection{rpiAdaptor, mqttAdaptor},
		[]gobot.Device{bme280, pir},
		work,
	)

	robot.Start()
}
