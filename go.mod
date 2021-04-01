module github.com/NOVAPokemon/gym

go 1.13

require (
	github.com/NOVAPokemon/utils v0.0.62
	github.com/golang/geo v0.0.0-20200319012246-673a6f80352d
	github.com/gorilla/mux v1.7.4
	github.com/gorilla/websocket v1.4.2
	github.com/mitchellh/mapstructure v1.3.3
	github.com/pkg/errors v0.8.1
	github.com/prometheus/client_golang v1.6.0
	github.com/sirupsen/logrus v1.5.0
	go.mongodb.org/mongo-driver v1.3.1
)

replace github.com/NOVAPokemon/utils v0.0.62 => ../utils
