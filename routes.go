package main

import (
	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/api"
)

const CreateRaidName = "CREATE_RAID"
const JoinRaidName = "JOIN_RAID"
const GetGymInfoName = "GET_GYM_INFO"
const CreateGymName = "CREATE_GYM"

const GET = "GET"
const POST = "POST"

var routes = utils.Routes{
	utils.Route{
		Name:        CreateRaidName,
		Method:      POST,
		Pattern:     api.CreateRaidRoute,
		HandlerFunc: handleCreateRaid,
	},

	utils.Route{
		Name:        JoinRaidName,
		Method:      GET,
		Pattern:     api.JoinRaidRoute,
		HandlerFunc: handleJoinRaid,
	},

	utils.Route{
		Name:        GetGymInfoName,
		Method:      GET,
		Pattern:     api.GetGymInfoRoute,
		HandlerFunc: handleGetGymInfo,
	},

	utils.Route{
		Name:        CreateGymName,
		Method:      POST,
		Pattern:     api.CreateGymRoute,
		HandlerFunc: handleCreateGym,
	},
}
