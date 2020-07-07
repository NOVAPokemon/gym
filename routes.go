package main

import (
	"strings"

	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/api"
)

const (
	createRaidName = "CREATE_RAID"
	joinRaidName   = "JOIN_RAID"
	getGymInfoName = "GET_GYM_INFO"
	createGymName  = "CREATE_GYM"
)

const (
	get  = "GET"
	post = "POST"
)

var routes = utils.Routes{
	api.GenStatusRoute(strings.ToLower(serviceName)),
	utils.Route{
		Name:        createRaidName,
		Method:      post,
		Pattern:     api.CreateRaidRoute,
		HandlerFunc: handleCreateRaid,
	},

	utils.Route{
		Name:        joinRaidName,
		Method:      get,
		Pattern:     api.JoinRaidRoute,
		HandlerFunc: handleJoinRaid,
	},

	utils.Route{
		Name:        getGymInfoName,
		Method:      get,
		Pattern:     api.GetGymInfoRoute,
		HandlerFunc: handleGetGymInfo,
	},

	utils.Route{
		Name:        createGymName,
		Method:      post,
		Pattern:     api.CreateGymRoute,
		HandlerFunc: handleCreateGym,
	},
}
