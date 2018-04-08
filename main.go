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
	"github.com/jacobsa/go-serial/serial"
	"github.com/spf13/viper"
	"gobot.io/x/gobot"
	"gobot.io/x/gobot/drivers/gpio"
	"gobot.io/x/gobot/drivers/i2c"
	"gobot.io/x/gobot/platforms/raspi"
)

type config struct {
	Mqtt struct {
		Broker  string
		Id      string
		LWT     string
		Preffix string
	}
	Bme280 struct {
		Enabled  bool
		Interval time.Duration
		Address  int
	}
	Ble struct {
		Enabled      bool
		Interval     time.Duration
		Duration     time.Duration
		MqttPreffix  string   `mapstructure:"mqtt_preffix"`
		KnownDevices []string `mapstructure:"known_devices"`
	}
	Pir struct {
		Enabled    bool
		Pin        string
		MqttSuffix string `mapstructure:"mqtt_suffix"`
	}
	Mhz19 struct {
		Enabled    bool
		Interval   time.Duration
		MqttSuffix string `mapstructure:"mqtt_suffix"`
		Port       string
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
	opts := mqtt.NewClientOptions().AddBroker(cfg.Mqtt.Broker).SetClientID(cfg.Mqtt.Id).SetAutoReconnect(true)
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

func readCO2() int {
	co2Req := []byte{0xFF, 0x01, 0x86, 0x00, 0x00, 0x00, 0x00, 0x00, 0x79}
	s, err := serial.Open(serial.OpenOptions{
		PortName:        cfg.Mhz19.Port,
		BaudRate:        9600,
		DataBits:        8,
		StopBits:        1,
		MinimumReadSize: 9,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	n, err := s.Write(co2Req)
	if err != nil {
		log.Fatal(err)
	}

	buf := make([]byte, 10)
	if n, err = s.Read(buf); err == nil && n == 9 {
		var sum int
		for i := 1; i < 8; i++ {
			sum += int(buf[i])
		}

		if ((sum%256)^0xFF)+1 == int(buf[8]) {
			return int(buf[2])*256 + int(buf[3])
		}
	}

	return -1
}

func x(suffix string) string {
	return cfg.Mqtt.Preffix + suffix
}

func main() {
	flag.Parse()

	//TODO compiler props?
	var bleDevice *linux.Device
	if cfg.Ble.Enabled {
		var err error
		if bleDevice, err = linux.NewDevice(); err != nil {
			log.Fatalf("Error initializing of a ble device : %s", err)
		}
	}

	rpiAdaptor := raspi.NewAdaptor()

	var bme280 *i2c.BME280Driver
	if cfg.Bme280.Enabled {
		bme280 = i2c.NewBME280Driver(rpiAdaptor, i2c.WithAddress(cfg.Bme280.Address))
	}

	var pir *gpio.PIRMotionDriver
	if cfg.Pir.Enabled {
		pir = gpio.NewPIRMotionDriver(rpiAdaptor, cfg.Pir.Pin)
	}

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
		if cfg.Ble.Enabled {
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
		}

		if cfg.Pir.Enabled {
			pir.On(gpio.MotionDetected, onMotionHandler)
			pir.On(gpio.MotionStopped, onMotionHandler)
		}

		if cfg.Bme280.Enabled {
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
		}

		if cfg.Mhz19.Enabled {
			gobot.Every(cfg.Mhz19.Interval, func() {
				if co2 := readCO2(); co2 != -1 {
					mqttClient.Publish(x(cfg.Mhz19.MqttSuffix), 1, false, strconv.Itoa(co2))
				}
			})
		}
	}

	var devices []gobot.Device
	if cfg.Bme280.Enabled {
		devices = append(devices, bme280)
	}
	if cfg.Pir.Enabled {
		devices = append(devices, pir)
	}
	robot := gobot.NewRobot("bot",
		[]gobot.Connection{rpiAdaptor},
		devices,
		work,
	)

	robot.Start()
}
