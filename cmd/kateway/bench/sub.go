package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/funkygao/gafka/cmd/kateway/api"
	"github.com/funkygao/gafka/ctx"
)

var (
	addr  string
	n     int
	appid string
	group string
	topic string
	step  int
	sleep time.Duration
)

func init() {
	ip, _ := ctx.LocalIP()
	flag.StringVar(&addr, "addr", fmt.Sprintf("http://%s:9192", ip), "sub kateway addr")
	flag.StringVar(&group, "g", "mygroup1", "consumer group name")
	flag.StringVar(&appid, "appid", "app1", "consume whose topic")
	flag.IntVar(&step, "step", 1, "display progress step")
	flag.StringVar(&topic, "t", "foobar", "topic to sub")
	flag.DurationVar(&sleep, "sleep", 0, "sleep between pub")
	flag.IntVar(&n, "n", 1000000, "run sub how many times")
	flag.Parse()
}

func main() {
	cf := api.DefaultConfig()
	cf.Debug = true
	c := api.NewClient("app2", cf)
	c.Connect(addr)
	i := 0
	t0 := time.Now()
	err := c.AckedSubscribe(appid, topic, "v1", group, func(statusCode int, msg []byte) error {
		i++
		if n > 0 && i >= n {
			return api.ErrSubStop
		}

		if i%step == 0 {
			log.Println(statusCode, string(msg))
		}

		if sleep > 0 {
			time.Sleep(sleep)
		}

		return nil
	})
	if err != nil {
		fmt.Println(err)
	}

	elapsed := time.Since(t0)
	fmt.Printf("%d msgs in %s, tps: %.2f\n", n, elapsed, float64(n)/elapsed.Seconds())
}
