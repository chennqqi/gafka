package influxdb

import (
	"errors"
	"net/url"
	"time"

	"github.com/funkygao/gafka/ctx"
)

type config struct {
	interval time.Duration
	hostname string // local host name

	url      url.URL
	database string // influxdb database
	username string // influxdb username
	password string
}

func NewConfig(uri, db, user, pass string, interval time.Duration) (*config, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	if interval == 0 {
		return nil, errors.New("illegal interval")
	}
	if uri == "" {
		return nil, errors.New("empty influxdb uri")
	}
	if db == "" {
		return nil, errors.New("empty influxdb db name")
	}

	return &config{
		hostname: ctx.Hostname(),
		url:      *u,
		database: db,
		username: user,
		password: pass,
		interval: interval,
	}, nil
}
