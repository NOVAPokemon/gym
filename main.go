package main

import (
	"github.com/NOVAPokemon/utils"
	gymDb "github.com/NOVAPokemon/utils/database/gym"
	"github.com/gorilla/websocket"
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

	if !*flags.DelayedComms {
		commsManager = utils.CreateDefaultCommunicationManager()
	} else {
		locationTag := utils.GetLocationTag(utils.DefaultLocationTagsFilename, serverName)
		commsManager = utils.CreateDefaultDelayedManager(locationTag, false)
	}

	gymDb.InitGymDBClient(*flags.ArchimedesEnabled)
	init_handlers()
	utils.StartServer(serviceName, host, port, routes, commsManager)
}
