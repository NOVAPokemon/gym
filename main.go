package main

import (
	"os"

	"github.com/NOVAPokemon/utils"
	gymDb "github.com/NOVAPokemon/utils/database/gym"
	http "github.com/bruno-anjos/archimedesHTTPClient"
	cedUtils "github.com/bruno-anjos/cloud-edge-deployment/pkg/utils"
	"github.com/golang/geo/s2"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	host        = utils.ServeHost
	port        = utils.GymPort
	serviceName = "GYM"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func main() {
	flags := utils.ParseFlags(serverName)

	if !*flags.LogToStdout {
		utils.SetLogFile(serverName)
	}

	location, exists := os.LookupEnv("LOCATION")
	if !exists {
		log.Fatal("no location in environment")
	}

	cellID := s2.CellIDFromToken(location)

	if !*flags.DelayedComms {
		commsManager = utils.CreateDefaultCommunicationManager()
	} else {
		commsManager = utils.CreateDefaultDelayedManager(false, &utils.OptionalConfigs{
			CellID: cellID,
		})
	}

	var node string
	node, exists = os.LookupEnv(cedUtils.NodeIPEnvVarName)
	if !exists {
		log.Panicf("no NODE_IP env var")
	} else {
		log.Infof("Node IP: %s", node)
	}

	httpClient.InitArchimedesClient(node, http.DefaultArchimedesPort, s2.CellIDFromToken(location).LatLng())

	gymDb.InitGymDBClient(*flags.ArchimedesEnabled)
	initHandlers()
	utils.StartServer(serviceName, host, port, routes, commsManager)
}
