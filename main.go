package main

import (
	"context"
	"flag"
	"log"
	"strconv"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"gobot.io/x/gobot"
	"gobot.io/x/gobot/drivers/gpio"
	"gobot.io/x/gobot/drivers/i2c"
	"gobot.io/x/gobot/platforms/mqtt"
	"gobot.io/x/gobot/platforms/raspi"
)

var (
	mqttAddress = flag.String("mqtt", "tcp://nuc.home:1883", "MQTT server address")
	du          = flag.Duration("du", 5*time.Second, "scanning duration")
)

func float32bytes(value float32) []byte {
	return strconv.AppendFloat(make([]byte, 0, 6), float64(value), 'f', 1, 32)
}

func main() {
	flag.Parse()

	d, err := linux.NewDevice()
	if err != nil {
		log.Fatalf("can't new device : %s", err)
	}

	rpiAdaptor := raspi.NewAdaptor()
	mqttAdaptor := mqtt.NewAdaptor(*mqttAddress, "rpi")
	mqttAdaptor.SetAutoReconnect(true)

	bme280 := i2c.NewBME280Driver(rpiAdaptor, i2c.WithAddress(0x76))
	pir := gpio.NewPIRMotionDriver(rpiAdaptor, "35")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	work := func() {
		gobot.Every(1*time.Minute, func() {
			mqttAdaptor.Publish("home/ground/heartbeat", []byte{})
			ctx, cancel := context.WithTimeout(ctx, *du)
			defer cancel()
			d.Scan(ctx, false, func(a ble.Advertisement) {
				mqttAdaptor.Publish("location/"+a.Addr().String(), []byte("home"))
			})
		})

		pir.On(gpio.MotionDetected, func(data interface{}) { mqttAdaptor.Publish("home/ground/pir", []byte("1")) })
		pir.On(gpio.MotionStopped, func(data interface{}) { mqttAdaptor.Publish("home/ground/pir", []byte("0")) })

		gobot.Every(5*time.Minute, func() {
			t, e := bme280.Temperature()
			if e == nil {
				mqttAdaptor.Publish("home/ground/temperature", float32bytes(t))
			}

			p, e := bme280.Pressure()
			if e == nil {
				mqttAdaptor.Publish("home/ground/pressure", float32bytes(p/100))
			}

			h, e := bme280.Humidity()
			if e == nil {
				mqttAdaptor.Publish("home/ground/humidity", float32bytes(h))
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
