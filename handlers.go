package main

import (
	"encoding/json"
	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/api"
	"github.com/NOVAPokemon/utils/clients"
	"github.com/NOVAPokemon/utils/items"
	"github.com/NOVAPokemon/utils/pokemons"
	"github.com/NOVAPokemon/utils/tokens"
	"github.com/NOVAPokemon/utils/websockets"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"io/ioutil"
	"math/rand"
	"net/http"
	"time"
)

const (
	DefaultGymsFile = "default_gyms.json"
	configFilename  = "configs.json"
)

// Pokemons taken from https://raw.githubusercontent.com/sindresorhus/pokemon/master/data/en.json
const PokemonsFile = "pokemons.json"

var (
	httpClient     *http.Client
	locationClient *clients.LocationClient
	gyms           map[string]*GymInternal
	pokemonSpecies []string
	config         *GymServerConfig
)

type GymInternal struct {
	Gym  *utils.Gym
	raid *RaidInternal
}

func init() {
	var err error

	config, err = loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	pokemonSpecies, err = loadPokemonSpecies()
	if err != nil {
		log.Fatal(err)
	}
	httpClient = &http.Client{}
	locationClient = clients.NewLocationClient(utils.LocationClientConfig{})

	gyms, err = loadGymsFromFile()
	if err != nil {
		log.Fatal(err)
	}
}

func loadConfig() (*GymServerConfig, error) {
	fileData, err := ioutil.ReadFile(configFilename)
	if err != nil {
		return nil, utils.WrapErrorLoadConfigs(err)
	}

	var config GymServerConfig
	err = json.Unmarshal(fileData, &config)
	if err != nil {
		return nil, utils.WrapErrorLoadConfigs(err)
	}

	log.Infof("Loaded config: %+v", config)

	return &config, nil
}

func handleCreateGym(w http.ResponseWriter, r *http.Request) {
	var gym = &utils.Gym{}

	err := json.NewDecoder(r.Body).Decode(gym)
	if err != nil {
		utils.LogAndSendHTTPError(&w, wrapCreateGymError(err), http.StatusBadRequest)
		return
	}

	newGymInternal := &GymInternal{
		Gym:  gym,
		raid: nil,
	}

	gyms[gym.Name] = newGymInternal
	go refreshRaidBossPeriodic(newGymInternal)

	err = locationClient.AddGymLocation(*gym)
	if err != nil {
		utils.LogAndSendHTTPError(&w, wrapCreateGymError(err), http.StatusInternalServerError)
		return
	}

	toSend, err := json.Marshal(gym)
	if err != nil {
		utils.LogAndSendHTTPError(&w, wrapCreateGymError(err), http.StatusInternalServerError)
		return
	}

	_, err = w.Write(toSend)
	if err != nil {
		utils.LogAndSendHTTPError(&w, wrapCreateGymError(err), http.StatusInternalServerError)
		return
	}
}

func handleCreateRaid(w http.ResponseWriter, r *http.Request) {
	var gymId = mux.Vars(r)[api.GymIdPathVar]

	gymInternal, ok := gyms[gymId]
	if !ok {
		err := wrapCreateRaidError(newNoGymFoundError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusNotFound)
		return
	}

	if gymInternal.Gym.RaidBoss == nil {
		err := wrapCreateRaidError(newGymNoRaidBossError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusBadRequest)
		return
	}

	if gymInternal.raid != nil {
		err := wrapCreateRaidError(newRaidAlreadyExistsError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusConflict)
		return
	}

	if gymInternal.Gym.RaidBoss.HP <= 0 {
		err := wrapCreateRaidError(newRaidBossDeadError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusBadRequest)
		return
	}

	startChan := make(chan struct{})
	trainersClient := clients.NewTrainersClient(httpClient)
	gymInternal.raid = NewRaid(
		primitive.NewObjectID(),
		config.PokemonsPerRaid,
		*gymInternal.Gym.RaidBoss,
		startChan,
		trainersClient,
		config.DefaultCooldown,
		config.TimeToStartRaid)

	gymInternal.Gym.RaidForming = true
	go handleRaidStart(gymInternal, startChan)
	go gymInternal.raid.Start()

	log.Info("Created new raid")
}

func handleRaidStart(gym *GymInternal, startChan chan struct{}) {
	gym.Gym.RaidForming = true
	<-startChan
	gym.Gym.RaidForming = false
	gym.raid = nil
}

func handleJoinRaid(w http.ResponseWriter, r *http.Request) {
	responseHeader := http.Header{}
	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		err = websockets.WrapUpgradeConnectionError(err)
		log.Error(wrapJoinRaidError(err))

		err = conn.Close()
		if err != nil {
			err = websockets.WrapClosingConnectionError(err)
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	authToken, err := tokens.ExtractAndVerifyAuthToken(r.Header)
	if err != nil {
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	trainersClient := clients.NewTrainersClient(httpClient)
	trainerItems, statsToken, pokemonsForBattle, err := extractAndVerifyTokensForBattle(trainersClient,
		authToken.Username, r)
	if err != nil {
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	var gymId = mux.Vars(r)[api.GymIdPathVar]
	gymInternal, ok := gyms[gymId]
	if !ok {
		err = newNoGymFoundError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	if gymInternal.raid == nil {
		err = newNoRaidInGymError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	if !gymInternal.raid.started {
		gymInternal.raid.AddPlayer(authToken.Username, pokemonsForBattle, statsToken, trainerItems, conn,
			r.Header.Get(tokens.AuthTokenHeaderName))
	} else {
		err = newRaidAlreadyStartedError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}
	}
}

func handleGetGymInfo(w http.ResponseWriter, r *http.Request) {
	var gymId = mux.Vars(r)[api.GymIdPathVar]

	gym, ok := gyms[gymId]
	if !ok {
		err := newNoGymFoundError(gymId)
		utils.LogAndSendHTTPError(&w, err, http.StatusNotFound)

		return
	}

	toSend, err := json.Marshal(gym.Gym)
	if err != nil {
		log.Error(wrapGetGymInfoError(err))
	}

	_, err = w.Write(toSend)
	if err != nil {
		log.Error(wrapGetGymInfoError(err))
	}
}

func refreshRaidBossPeriodic(gymInternal *GymInternal) {
	gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP,
		config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
	log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Info("Refreshing boss...")
			gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP,
				config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
			log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)
		}
	}
}

func extractAndVerifyTokensForBattle(trainersClient *clients.TrainersClient, username string,
	r *http.Request) (map[string]items.Item, *utils.TrainerStats, map[string]*pokemons.Pokemon, error) {
	pokemonTkns, err := tokens.ExtractAndVerifyPokemonTokens(r.Header)
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	if len(pokemonTkns) > config.PokemonsPerRaid {
		return nil, nil, nil, wrapTokensForBattleError(ErrorTooManyPokemons)
	}

	if len(pokemonTkns) < config.PokemonsPerRaid {
		return nil, nil, nil, wrapTokensForBattleError(ErrorNotEnoughPokemons)
	}

	pokemonsInToken := make(map[string]*pokemons.Pokemon, len(pokemonTkns))
	pokemonHashes := make(map[string][]byte, len(pokemonTkns))
	for _, pokemonTkn := range pokemonTkns {
		pokemonId := pokemonTkn.Pokemon.Id.Hex()
		pokemonsInToken[pokemonId] = &pokemonTkn.Pokemon
		pokemonHashes[pokemonId] = pokemonTkn.PokemonHash
	}

	valid, err := trainersClient.VerifyPokemons(username, pokemonHashes, r.Header.Get(tokens.AuthTokenHeaderName))
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	if !*valid {
		return nil, nil, nil, wrapTokensForBattleError(tokens.ErrorInvalidPokemonTokens)
	}

	// stats
	trainerStatsToken, err := tokens.ExtractAndVerifyTrainerStatsToken(r.Header)
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	valid, err = trainersClient.VerifyTrainerStats(username, trainerStatsToken.TrainerHash,
		r.Header.Get(tokens.AuthTokenHeaderName))
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	if !*valid {
		return nil, nil, nil, wrapTokensForBattleError(tokens.ErrorInvalidStatsToken)
	}

	// items

	itemsToken, err := tokens.ExtractAndVerifyItemsToken(r.Header)
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	valid, err = trainersClient.VerifyItems(username, itemsToken.ItemsHash, r.Header.Get(tokens.AuthTokenHeaderName))
	if err != nil {
		return nil, nil, nil, wrapTokensForBattleError(err)
	}

	if !*valid {
		return nil, nil, nil, wrapTokensForBattleError(tokens.ErrorInvalidItemsToken)
	}

	return itemsToken.Items, &trainerStatsToken.TrainerStats, pokemonsInToken, nil
}

func loadGymsFromFile() (map[string]*GymInternal, error) {
	data, err := ioutil.ReadFile(DefaultGymsFile)
	if err != nil {
		return nil, wrapLoadGymsError(err)
	}

	var gymsArr []*utils.Gym
	err = json.Unmarshal(data, &gymsArr)
	if err != nil {
		return nil, wrapLoadGymsError(err)
	}

	var gymsMap = make(map[string]*GymInternal, len(gymsArr))
	for _, gym := range gymsArr {
		newGymInternal := &GymInternal{
			Gym:  gym,
			raid: nil,
		}
		gymsMap[gym.Name] = newGymInternal
		go refreshRaidBossPeriodic(newGymInternal)
		err = locationClient.AddGymLocation(*gym)
		if err != nil {
			log.Error(wrapLoadGymsError(err))
		}
	}

	log.Infof("Loaded %d gyms.", len(gymsMap))
	return gymsMap, nil
}

func loadPokemonSpecies() ([]string, error) {
	data, err := ioutil.ReadFile(PokemonsFile)
	if err != nil {
		return nil, wrapLoadSpecies(err)
	}

	var pokemonNames []string
	err = json.Unmarshal(data, &pokemonNames)
	if err != nil {
		return nil, wrapLoadSpecies(err)
	}

	log.Infof("Loaded %d pokemon species.", len(pokemonNames))

	return pokemonNames, nil
}

func writeErrorMessageAndClose(conn *websocket.Conn, err error) error {
	err = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
	if err != nil {
		return websockets.WrapWritingMessageError(err)
	}

	err = conn.Close()
	if err != nil {
		return websockets.WrapClosingConnectionError(err)
	}

	return nil
}
