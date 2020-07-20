package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/api"
	"github.com/NOVAPokemon/utils/clients"
	gymDb "github.com/NOVAPokemon/utils/database/gym"
	"github.com/NOVAPokemon/utils/items"
	"github.com/NOVAPokemon/utils/pokemons"
	"github.com/NOVAPokemon/utils/tokens"
	"github.com/NOVAPokemon/utils/websockets"
	"github.com/golang/geo/s2"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	gymConfigsFolder = "gymConfigs"
)

type (
	gymsMapType = gymInternalType
)

// pokemonsFile taken from https://raw.githubusercontent.com/sindresorhus/pokemon/master/data/en.json
const pokemonsFile = "pokemons.json"
const configFilename = "configs.json"

var (
	httpClient          *http.Client
	locationClient      *clients.LocationClient
	gyms                sync.Map
	pokemonSpecies      []string
	config              *gymServerConfig
	serverName          string
	serverNr            int64
	serviceNameHeadless string
	commsManager        websockets.CommunicationManager
)

type gymInternalType struct {
	Gym  utils.Gym
	raid *raidInternal
}

func init() {
	var err error

	if aux, exists := os.LookupEnv(utils.HeadlessServiceNameEnvVar); exists {
		serviceNameHeadless = aux
	} else {
		log.Fatal("Could not load headless service name")
	}

	httpClient = &http.Client{}
	if pokemonSpecies, err = loadPokemonSpecies(); err != nil {
		log.Fatal(err)
	}
	if err = loadConfig(); err != nil {
		log.Fatal(err)
	}

	if aux, exists := os.LookupEnv(utils.HostnameEnvVar); exists {
		serverName = aux
	} else {
		log.Fatal("Could not load server name")
	}
	split := strings.Split(serverName, "-")
	if serverNr, err = strconv.ParseInt(split[len(split)-1], 10, 32); err != nil {
		log.Fatal(err)
	}
	log.Infof("Server name :%s; ServerNr: %d", serverName, serverNr)
}

func init_handlers() {
	locationClient = clients.NewLocationClient(utils.LocationClientConfig{}, commsManager)

	var err error
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(5*i) * time.Second)

		var gymsLoaded []utils.GymWithServer
		gymsLoaded, err = loadGymsFromDBForServer(serverName)
		if err != nil {
			log.Warn(err)
			if serverNr == 0 {
				// if configs are missing, server 0 adds them
				err = loadGymsToDb()
				if err != nil {
					log.Warn(wrapInit(err))
					continue
				}

				i--
			}
		} else {
			err = registerGyms(gymsLoaded)
			if err != nil {
				log.Error(wrapInit(err))
				continue
			}

			go refreshGymsPeriodic()
			return
		}

	}

	panic("Could not load gyms")
}

func refreshGymsPeriodic() {
	for {
		_, err := loadGymsFromDBForServer(serverName)
		if err != nil {
			panic(err)
		}

		log.Info("Active gyms:")
		gyms.Range(func(key, value interface{}) bool {
			log.Infof("Gym name: %s, Gym: %+v", key, value)
			return true
		})
		time.Sleep(30 * time.Second)
	}
}

func loadConfig() error {
	fileData, err := ioutil.ReadFile(configFilename)
	if err != nil {
		return utils.WrapErrorLoadConfigs(err)
	}
	err = json.Unmarshal(fileData, &config)
	if err != nil {
		return utils.WrapErrorLoadConfigs(err)
	}
	log.Infof("Loaded config: %+v", config)
	return nil
}

func loadGymsToDb() (err error) {
	type gymInDegrees struct {
		Name      string `json:"name" bson:"name,omitempty"`
		Latitude  float64
		Longitude float64
	}
	var files []os.FileInfo
	if files, err = ioutil.ReadDir(gymConfigsFolder); err != nil {
		return wrapLoadGymsToDBError(err)
	}

	for _, file := range files {
		if !strings.Contains(file.Name(), ".json") {
			continue
		}
		log.Infof("Doing file: %s", file.Name())
		var fileData []byte
		if fileData, err = ioutil.ReadFile(fmt.Sprintf("%s/%s", gymConfigsFolder, file.Name())); err != nil {
			return wrapLoadGymsToDBError(err)
		}

		var gymsInFile []gymInDegrees
		if err = json.Unmarshal(fileData, &gymsInFile); err != nil {
			return wrapLoadGymsToDBError(err)
		}

		gymsInLatLng := make([]utils.Gym, len(gymsInFile))
		for i, gym := range gymsInFile {
			gymsInLatLng[i] = utils.Gym{
				Name:     gym.Name,
				Location: s2.LatLngFromDegrees(gym.Latitude, gym.Longitude),
			}
		}

		currServerName := strings.TrimSuffix(file.Name(), ".json")
		for _, gym := range gymsInLatLng {
			gymsForServer := utils.GymWithServer{
				Gym:        gym,
				ServerName: currServerName,
			}
			if err = gymDb.AddGymWithServer(gymsForServer); err != nil {
				return wrapLoadGymsToDBError(err)
			}
			log.Infof("Loaded gym to database: %s", gym.Name)
		}
		log.Infof("Loaded gyms to database for server %s", currServerName)
	}

	return nil
}

func loadGymsFromDBForServer(serverName string) ([]utils.GymWithServer, error) {
	gymsWithSrv, err := gymDb.GetGymsForServer(serverName)
	if err != nil {
		return nil, wrapLoadGymsFromDBError(err)
	}

	for _, gymWithSrv := range gymsWithSrv {
		newGymInternal := gymInternalType{
			Gym:  gymWithSrv.Gym,
			raid: nil,
		}

		if _, loaded := gyms.LoadOrStore(gymWithSrv.Gym.Name, newGymInternal); !loaded {
			go refreshRaidBossPeriodic(gymWithSrv.Gym.Name)
		}
	}

	return gymsWithSrv, nil
}

func registerGyms(gymsWithSrv []utils.GymWithServer) error {
	for _, gymWithSrv := range gymsWithSrv {
		gymWithServer := utils.GymWithServer{
			ServerName: fmt.Sprintf("%s.%s", gymWithSrv.ServerName, serviceNameHeadless),
			Gym:        gymWithSrv.Gym,
		}

		log.Infof("Registering gym %s with location server", gymWithSrv.Gym.Name)
		err := locationClient.AddGymLocation(gymWithServer)
		if err != nil {
			return wrapLoadGymsFromDBError(err)
		}

		log.Info("Done!")
	}

	return nil
}

func handleCreateGym(w http.ResponseWriter, r *http.Request) {
	var gym = utils.Gym{}

	err := json.NewDecoder(r.Body).Decode(&gym)
	if err != nil {
		utils.LogAndSendHTTPError(&w, wrapCreateGymError(err), http.StatusBadRequest)
		return
	}

	newGymInternal := gymInternalType{
		Gym:  gym,
		raid: nil,
	}
	gyms.Store(gym.Name, newGymInternal)
	go refreshRaidBossPeriodic(gym.Name)

	gymWithServer := utils.GymWithServer{
		ServerName: fmt.Sprintf("%s.%s", serverName, serviceNameHeadless),
		Gym:        gym,
	}

	err = locationClient.AddGymLocation(gymWithServer)
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

	value, ok := gyms.Load(gymId)
	if !ok {
		err := wrapCreateRaidError(newNoGymFoundError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusNotFound)
		return
	}
	gymInternal := value.(gymsMapType)
	if gymInternal.Gym.RaidBoss == nil {
		err := wrapCreateRaidError(newGymNoRaidBossError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusBadRequest)
		return
	}

	if gymInternal.raid != nil {
		err := wrapCreateRaidError(newRaidAlreadyExistsError(gymId))
		utils.LogWarnAndSendHTTPError(&w, err, http.StatusConflict)
		return
	}

	if gymInternal.Gym.RaidBoss.HP <= 0 {
		err := wrapCreateRaidError(newRaidBossDeadError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusBadRequest)
		return
	}

	trainersClient := clients.NewTrainersClient(httpClient, commsManager)
	gymInternal.raid = newRaid(
		primitive.NewObjectID(),
		config.MaxTrainersPerRaid,
		*gymInternal.Gym.RaidBoss,
		trainersClient,
		config.DefaultCooldown,
		commsManager)
	gymInternal.Gym.RaidForming = true
	gyms.Store(gymId, gymInternal)
	go handleRaidStart(gymId, gymInternal)
	log.Info("Created new raid")
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

		err = writeErrorMessageAndClose(conn, err, commsManager)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	trainersClient := clients.NewTrainersClient(httpClient, commsManager)
	trainerItems, statsToken, pokemonsForBattle, err := extractAndVerifyTokensForBattle(trainersClient,
		authToken.Username, r)
	if err != nil {
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err, commsManager)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	var gymId = mux.Vars(r)[api.GymIdPathVar]
	value, ok := gyms.Load(gymId)
	if !ok {
		err = newNoGymFoundError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err, commsManager)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	gymInternal := value.(gymsMapType)
	if gymInternal.raid == nil {
		err = newNoRaidInGymError(gymId)
		log.Warn(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err, commsManager)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}
		return
	}

	_, err = gymInternal.raid.addPlayer(authToken.Username, pokemonsForBattle, statsToken, trainerItems, conn, r.Header.Get(tokens.AuthTokenHeaderName))
	if err != nil {
		if errors.Cause(err) == websockets.ErrorLobbyIsFull ||
			errors.Cause(err) == websockets.ErrorLobbyAlreadyFinished {
			log.Warn(wrapJoinRaidError(err))
		} else {
			log.Error(wrapJoinRaidError(err))
		}
		err = writeErrorMessageAndClose(conn, err, commsManager)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}
	}
}

func handleRaidStart(gymId string, gym gymInternalType) {
	startTimer := time.NewTimer(time.Duration(config.TimeToStartRaid) * time.Millisecond)
	<-startTimer.C
	go gym.raid.start()
	gym.Gym.RaidForming = false
	gym.raid = nil
	gyms.Store(gymId, gym)
}

func handleGetGymInfo(w http.ResponseWriter, r *http.Request) {
	var gymId = mux.Vars(r)[api.GymIdPathVar]

	value, ok := gyms.Load(gymId)
	if !ok {
		err := newNoGymFoundError(gymId)
		utils.LogAndSendHTTPError(&w, err, http.StatusNotFound)

		return
	}

	gymInternal := value.(gymsMapType)
	toSend, err := json.Marshal(gymInternal.Gym)
	if err != nil {
		log.Error(wrapGetGymInfoError(err))
	}

	_, err = w.Write(toSend)
	if err != nil {
		log.Error(wrapGetGymInfoError(err))
	}
}

func refreshRaidBossPeriodic(gymName string) {
	value, ok := gyms.Load(gymName)
	if !ok {
		log.Infof("Routine generating raidboss for %s exiting", gymName)
		return
	}
	gymInternal := value.(gymInternalType)
	gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP,
		config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
	gyms.Store(gymName, gymInternal)
	log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			value, ok = gyms.Load(gymName)
			if !ok {
				log.Infof("Routine generating raidboss for %s exiting", gymName)
				return
			}
			gymInternal = value.(gymInternalType)
			gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP,
				config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
			gyms.Store(gymName, gymInternal)
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
		return nil, nil, nil, wrapTokensForBattleError(errorTooManyPokemons)
	}

	if len(pokemonTkns) < config.PokemonsPerRaid {
		return nil, nil, nil, wrapTokensForBattleError(errorNotEnoughPokemons)
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

func loadPokemonSpecies() ([]string, error) {
	data, err := ioutil.ReadFile(pokemonsFile)
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

func writeErrorMessageAndClose(conn *websocket.Conn, msgErr error, writer websockets.CommunicationManager) error {
	errMsg := websockets.ErrorMessage{
		Info:  msgErr.Error(),
		Fatal: false,
	}

	serializedMsg := websockets.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(errMsg.SerializeToWSMessage().Serialize()),
	}

	err := writer.WriteGenericMessageToConn(conn, serializedMsg)
	if err != nil {
		return websockets.WrapWritingMessageError(err)
	}

	err = conn.Close()
	if err != nil {
		return websockets.WrapClosingConnectionError(err)
	}

	return nil
}
