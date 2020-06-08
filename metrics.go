package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	nrRaidsStarted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gym_raids_started",
		Help: "The total number of started raids",
	})
	nrRaidsFinished = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gym_raids_finished",
		Help: "The total number of finished raids",
	})
)

func emitRaidStart() {
	nrRaidsStarted.Inc()
}

func emitRaidFinish() {
	nrRaidsFinished.Inc()
}

func emitActiveGyms() {
	nrRaidsFinished.Inc()
}
