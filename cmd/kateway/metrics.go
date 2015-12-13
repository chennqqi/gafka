package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/funkygao/gafka/ctx"
	"github.com/rcrowley/go-metrics"
)

func startRuntimeMetrics(interval time.Duration) {
	metrics.RegisterDebugGCStats(metrics.DefaultRegistry)
	metrics.RegisterRuntimeMemStats(metrics.DefaultRegistry)

	go metrics.CaptureDebugGCStats(metrics.DefaultRegistry, interval)
	go metrics.CaptureRuntimeMemStats(metrics.DefaultRegistry, interval)
}

type subMetrics struct {
	ClientError metrics.Counter
}

func newSubMetrics(interval time.Duration) *subMetrics {
	this := &subMetrics{
		ClientError: metrics.NewCounter(),
	}
	return this
}

type pubMetrics struct {
	PubOkMap   map[string]metrics.Counter
	pubOkMu    sync.RWMutex
	PubFailMap map[string]metrics.Counter
	pubFailMu  sync.RWMutex

	// BytesInPerSec, BytesOutPerSec, FailedMessagesPerSec
	ConnAccept    metrics.Counter
	ClientError   metrics.Counter
	PubConcurrent metrics.Counter
	PubQps        metrics.Meter
	PubLatency    metrics.Histogram
	PubMsgSize    metrics.Histogram // FIXME
}

func newPubMetrics(interval time.Duration) *pubMetrics {
	this := &pubMetrics{
		PubOkMap:   make(map[string]metrics.Counter),
		PubFailMap: make(map[string]metrics.Counter),

		ConnAccept:    metrics.NewRegisteredCounter("pub.accept", metrics.DefaultRegistry),
		PubConcurrent: metrics.NewRegisteredCounter("pub.concurrent", metrics.DefaultRegistry),
		ClientError:   metrics.NewRegisteredCounter("pub.clienterr", metrics.DefaultRegistry),
		PubQps:        metrics.NewRegisteredMeter("pub.qps", metrics.DefaultRegistry),
		PubMsgSize:    metrics.NewRegisteredHistogram("pub.msgsize", metrics.DefaultRegistry, metrics.NewExpDecaySample(1028, 0.015)),
		PubLatency:    metrics.NewRegisteredHistogram("pub.latency", metrics.DefaultRegistry, metrics.NewExpDecaySample(1028, 0.015)),
	}

	go runMetricsReporter(metrics.DefaultRegistry, interval*60)

	// influxdb reporter
	if options.influxServer != "" {
		go InfluxDB(ctx.Hostname(), metrics.DefaultRegistry, interval,
			options.influxServer, "kateway1", "", "") // FIXME
	}

	return this
}

func (this *pubMetrics) recordForApp(appid, topic, ver, name string,
	mu *sync.RWMutex, m map[string]metrics.Counter) {
	tag := fmt.Sprintf("{%s.%s.%s}", appid, topic, ver)
	mu.RLock()
	counter, present := m[tag]
	mu.RUnlock()

	if present {
		counter.Inc(1)
		return
	}

	mu.Lock()
	m[tag] = metrics.NewRegisteredCounter(tag+name, metrics.DefaultRegistry)
	mu.Unlock()

	m[tag].Inc(1)
}

func (this *pubMetrics) pubFail(appid, topic, ver string) {
	this.recordForApp(appid, topic, ver, "pub.fail", &this.pubFailMu, this.PubFailMap)
}

func (this *pubMetrics) pubOk(appid, topic, ver string) {
	this.recordForApp(appid, topic, ver, "pub.ok", &this.pubOkMu, this.PubOkMap)
}
