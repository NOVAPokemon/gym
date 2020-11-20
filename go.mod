module github.com/NOVAPokemon/gym

go 1.13

require (
	github.com/NOVAPokemon/utils v0.0.62
	github.com/bruno-anjos/archimedesHTTPClient v0.0.2
	github.com/golang/geo v0.0.0-20200730024412-e86565bf3f35
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.4.2
	github.com/mitchellh/mapstructure v1.3.3
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.6.0
	github.com/sirupsen/logrus v1.6.0
	go.mongodb.org/mongo-driver v1.3.1
)

replace (
	github.com/NOVAPokemon/utils v0.0.62 => ../utils
	github.com/bruno-anjos/archimedesHTTPClient v0.0.2 => ./../../bruno-anjos/archimedesHTTPClient
	github.com/bruno-anjos/cloud-edge-deployment v0.0.1 => ../../bruno-anjos/cloud-edge-deployment
)
