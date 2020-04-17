package main

import (
	"fmt"
	"github.com/NOVAPokemon/utils"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"math/rand"
	"net/http"
	"time"
)

const host = utils.Host
const port = utils.GymPort

var addr = fmt.Sprintf("%s:%d", host, port)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func main() {
	rand.Seed(time.Now().Unix())
	r := utils.NewRouter(routes)
	log.Infof("Starting GYM server in port %d...\n", port)
	log.Fatal(http.ListenAndServe(addr, r))
}
