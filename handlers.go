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
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	GymConfigsFolder = "gymConfigs"
)

type (
	gymsMapType = GymInternal
)

// Pokemons taken from https://raw.githubusercontent.com/sindresorhus/pokemon/master/data/en.json
const PokemonsFile = "pokemons.json"
const configFilename = "configs.json"

var (
	httpClient          *http.Client
	locationClient      *clients.LocationClient
	gyms                sync.Map
	pokemonSpecies      []string
	config              *GymServerConfig
	serverName          string
	serverNr            int64
	serviceNameHeadless string
)

type GymInternal struct {
	Gym  utils.Gym
	raid *RaidInternal
}

func init() {
	var err error

	if aux, exists := os.LookupEnv(utils.HeadlessServiceNameEnvVar); exists {
		serviceNameHeadless = aux
	} else {
		log.Fatal("Could not load headless service name")
	}

	httpClient = &http.Client{}
	locationClient = clients.NewLocationClient(utils.LocationClientConfig{})
	if pokemonSpecies, err = loadPokemonSpecies(); err != nil {
		log.Fatal(err)
	}
	if config, err = loadConfig(); err != nil {
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

	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(5*i) * time.Second)

		err := loadGymsFromDBForServer(serverName)
		if err != nil {
			log.Error(err)
			if serverNr == 0 {
				// if configs are missing, server 0 adds them
				err := loadGymsToDb()
				if err != nil {
					log.Error(WrapInit(err))
					continue
				}

				gymsWithSrv, err := getGymsFromDB()
				if err != nil {
					log.Error(WrapInit(err))
					continue
				}

				err = registerGyms(gymsWithSrv)
				if err != nil {
					log.Error(WrapInit(err))
					continue
				}

				i--
			}
		} else {
			go logGymsPeriodic()
			return
		}

	}

	panic("Could not load gyms")
}

func logGymsPeriodic() {
	for {
		log.Info("Active gyms:")
		gyms.Range(func(key, value interface{}) bool {
			log.Infof("Gym name: %s, Gym: %+v", key, value)
			return true
		})
		time.Sleep(30 * time.Second)
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

func loadGymsToDb() error {
	type GymInDegrees struct {
		Name      string `json:"name" bson:"name,omitempty"`
		Latitude  float64
		Longitude float64
	}

	files, err := ioutil.ReadDir(GymConfigsFolder)
	if err != nil {
		return wrapLoadGymsToDBError(err)
	}

	for _, file := range files {
		if !strings.Contains(file.Name(), ".json") {
			continue
		}
		log.Infof("Doing file: %s", file.Name())
		fileData, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", GymConfigsFolder, file.Name()))
		if err != nil {
			return wrapLoadGymsToDBError(err)
		}

		var gyms []GymInDegrees
		if err = json.Unmarshal(fileData, &gyms); err != nil {
			return wrapLoadGymsToDBError(err)
		}

		gymsInLatLng := make([]utils.Gym, len(gyms))
		for i, gym := range gyms {
			gymsInLatLng[i] = utils.Gym{
				Name:     gym.Name,
				Location: s2.LatLngFromDegrees(gym.Latitude, gym.Longitude),
			}
		}

		serverName := strings.TrimSuffix(file.Name(), ".json")
		for _, gym := range gymsInLatLng {
			gymsForServer := utils.GymWithServer{
				Gym:        gym,
				ServerName: serverName,
			}
			if err = gymDb.AddGymWithServer(gymsForServer); err != nil {
				return wrapLoadGymsToDBError(err)
			}
			log.Infof("Loaded gym to database: %s", gym.Name)
		}
		log.Infof("Loaded gyms to database for server %s", serverName)
	}

	return nil
}

func getGymsFromDB() ([]utils.GymWithServer, error) {
	gymsWithSrv, err := gymDb.GetAllGyms()
	if err != nil {
		return nil, wrapLoadGymsFromDBError(err)
	}

	return gymsWithSrv, nil
}

func loadGymsFromDBForServer(serverName string) error {
	gymsWithSrv, err := gymDb.GetGymsForServer(serverName)
	if err != nil {
		return wrapLoadGymsFromDBError(err)
	}

	for _, gymWithSrv := range gymsWithSrv {
		newGymInternal := GymInternal{
			Gym:  gymWithSrv.Gym,
			raid: nil,
		}

		gyms.Store(gymWithSrv.Gym.Name, newGymInternal)
		go refreshRaidBossPeriodic(gymWithSrv.Gym.Name)
	}

	return nil
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

	newGymInternal := GymInternal{
		Gym:  gym,
		raid: nil,
	}
	gyms.Store(gym.Name, newGymInternal)
	go refreshRaidBossPeriodic(gym.Name)

	gymWithServer := utils.GymWithServer{
		ServerName: serverName,
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
		utils.LogAndSendHTTPError(&w, err, http.StatusConflict)
		return
	}

	if gymInternal.Gym.RaidBoss.HP <= 0 {
		err := wrapCreateRaidError(newRaidBossDeadError(gymId))
		utils.LogAndSendHTTPError(&w, err, http.StatusBadRequest)
		return
	}

	trainersClient := clients.NewTrainersClient(httpClient)
	gymInternal.raid = NewRaid(
		primitive.NewObjectID(),
		config.MaxTrainersPerRaid,
		*gymInternal.Gym.RaidBoss,
		trainersClient,
		config.DefaultCooldown)
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
	value, ok := gyms.Load(gymId)
	if !ok {
		err = newNoGymFoundError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}

		return
	}

	gymInternal := value.(gymsMapType)
	if gymInternal.raid == nil {
		err = newNoRaidInGymError(gymId)
		log.Error(wrapJoinRaidError(err))

		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}
		return
	}

	_, err = gymInternal.raid.AddPlayer(authToken.Username, pokemonsForBattle, statsToken, trainerItems, conn, r.Header.Get(tokens.AuthTokenHeaderName))
	if err != nil {
		log.Error(wrapJoinRaidError(err))
		err = writeErrorMessageAndClose(conn, err)
		if err != nil {
			log.Error(wrapJoinRaidError(err))
		}
	}
}

func handleRaidStart(gymId string, gym GymInternal) {
	startTimer := time.NewTimer(time.Duration(config.TimeToStartRaid) * time.Millisecond)
	<-startTimer.C
	go gym.raid.Start()
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
	gymInternal := value.(GymInternal)
	gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP,
		config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
	gyms.Store(gymName, gymInternal)
	log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			value, ok := gyms.Load(gymName)
			if !ok {
				log.Infof("Routine generating raidboss for %s exiting", gymName)
				return
			}
			gymInternal := value.(GymInternal)
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
