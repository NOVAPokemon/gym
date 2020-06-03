package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"time"
)

var (
	nrRaidsStarted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "raids_started",
		Help: "The total number of started raids",
	})
	nrRaidsFinished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "raids_started",
		Help: "The total number of started raids",
	})
)

func emitRaidStart() {
	nrRaidsStarted.Inc()
}


func emitRaidFinish() {
	nrRaidsFinished.Inc()
}

// metrics for prometheus
func recordMetrics() {
	go func() {
		for {
			emitRaidStart()
			time.Sleep(2 * time.Second)
		}
	}()
}
