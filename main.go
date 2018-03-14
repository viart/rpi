package main

import (
	"context"
	"flag"
	"log"
	"strconv"
	"time"

	"github.com/eclipse/paho.mqtt.golang"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/spf13/viper"
	"gobot.io/x/gobot"
	"gobot.io/x/gobot/drivers/gpio"
	"gobot.io/x/gobot/drivers/i2c"
	"gobot.io/x/gobot/platforms/raspi"
)

type config struct {
	Mqtt struct {
		Broker  string
		LWT     string
		Preffix string
	}
	Bme280 struct {
		Interval time.Duration
		Address  int
	}
	Ble struct {
		Interval     time.Duration
		Duration     time.Duration
		MqttPreffix  string   `mapstructure:"mqtt_preffix"`
		KnownDevices []string `mapstructure:"known_devices"`
	}
	Pir struct {
		Pin        string
		MqttSuffix string `mapstructure:"mqtt_suffix"`
	}
}

var (
	cfg        config
	mqttClient mqtt.Client
	configFile = flag.String("config", "config", "Config file")
)

func float32bytes(value float32) []byte {
	return strconv.AppendFloat(make([]byte, 0, 6), float64(value), 'f', 1, 32)
}

func init() {
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/gobot/")
	viper.SetConfigName(*configFile)

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading a config file, %s", err)
	}

	err := viper.Unmarshal(&cfg)
	if err != nil {
		log.Fatalf("Error decoding into a config struct, %v", err)
	}

	mqttClient = initMqtt()
}

//TODO: proper shutdown + keep-alive?
func initMqtt() mqtt.Client {
	opts := mqtt.NewClientOptions().AddBroker(cfg.Mqtt.Broker).SetClientID("rpi").SetAutoReconnect(true)
	if cfg.Mqtt.LWT != "" {
		opts.SetBinaryWill(cfg.Mqtt.LWT, []byte("0"), 1, true)
		// inspired by https://www.hivemq.com/blog/mqtt-essentials-part-9-last-will-and-testament
		opts.SetOnConnectHandler(func(c mqtt.Client) {
			c.Publish(cfg.Mqtt.LWT, 1, true, []byte("1"))
		})
	}

	c := mqtt.NewClient(opts)
	if token := c.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	return c
}

func x(suffix string) string {
	return cfg.Mqtt.Preffix + suffix
}

func main() {
	flag.Parse()

	bleDevice, err := linux.NewDevice()
	if err != nil {
		log.Fatalf("Error initializing of a ble device : %s", err)
	}

	rpiAdaptor := raspi.NewAdaptor()
	bme280 := i2c.NewBME280Driver(rpiAdaptor, i2c.WithAddress(cfg.Bme280.Address)) // 0x76
	pir := gpio.NewPIRMotionDriver(rpiAdaptor, cfg.Pir.Pin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	onMotionHandler := func(data interface{}) {
		val := []byte("0")
		if data == 1 {
			val = []byte("1")
		}
		mqttClient.Publish(x(cfg.Pir.MqttSuffix), 1, false, val)
	}

	work := func() {
		gobot.Every(cfg.Ble.Interval, func() {
			ctx, cancel := context.WithTimeout(ctx, cfg.Ble.Duration)
			defer cancel()
			bleDevice.Scan(ctx, false, func(a ble.Advertisement) {
				for _, item := range cfg.Ble.KnownDevices {
					if item == a.Addr().String() {
						mqttClient.Publish(cfg.Ble.MqttPreffix+item, 1, false, []byte("home"))
					}
				}
			})
		})

		pir.On(gpio.MotionDetected, onMotionHandler)
		pir.On(gpio.MotionStopped, onMotionHandler)

		gobot.Every(cfg.Bme280.Interval, func() {
			if t, err := bme280.Temperature(); err == nil {
				mqttClient.Publish(x("temperature"), 1, false, float32bytes(t))
			}

			if p, err := bme280.Pressure(); err == nil {
				mqttClient.Publish(x("pressure"), 1, false, float32bytes(p/100))
			}

			if h, err := bme280.Humidity(); err == nil {
				mqttClient.Publish(x("humidity"), 1, false, float32bytes(h))
			}
		})

		// mqttAdaptor.On("hello", func(msg mqtt.Message) { fmt.Println(msg) })
	}

	robot := gobot.NewRobot("bot",
		[]gobot.Connection{rpiAdaptor},
		[]gobot.Device{bme280, pir},
		work,
	)

	robot.Start()
}
