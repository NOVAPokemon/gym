package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/api"
	"github.com/NOVAPokemon/utils/clients"
	"github.com/NOVAPokemon/utils/items"
	"github.com/NOVAPokemon/utils/pokemons"
	"github.com/NOVAPokemon/utils/tokens"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"io/ioutil"
	"math/rand"
	"net/http"
	"time"
)

const DefaultGymsFile = "default_gyms.json"
const configFilename = "configs.json"

// Pokemons taken from https://raw.githubusercontent.com/sindresorhus/pokemon/master/data/en.json
const PokemonsFile = "pokemons.json"

var (
	ErrNotEnoughPokemons    = errors.New("not enough pokemons")
	ErrTooManyPokemons      = errors.New("not enough pokemons")
	ErrInvalidPokemonHashes = errors.New("invalid pokemon hashes")

	config         = loadConfig()
	pokemonSpecies = loadPokemonSpecies()
)

var httpClient *http.Client
var locationClient *clients.LocationClient
var gyms map[string]*GymInternal

type GymInternal struct {
	Gym  *utils.Gym
	raid *RaidInternal
}

func init() {
	httpClient = &http.Client{}
	locationClient = clients.NewLocationClient(utils.LocationClientConfig{})
	gyms = loadGymsFromFile()
}

func loadConfig() *GymServerConfig {
	fileData, err := ioutil.ReadFile(configFilename)
	if err != nil {
		log.Panic(err)
		return nil
	}

	var config GymServerConfig
	err = json.Unmarshal(fileData, &config)
	if err != nil {
		log.Panic(err)
		return nil
	}

	log.Infof("Loaded config: %+v", config)

	return &config
}

func handleCreateGym(w http.ResponseWriter, r *http.Request) {
	log.Infof("Request to add gym")
	var gym = &utils.Gym{}
	err := json.NewDecoder(r.Body).Decode(gym)
	if err != nil {
		http.Error(w, "Invalid gym", http.StatusBadRequest)
		return
	}
	newGymInternal := &GymInternal{
		Gym:  gym,
		raid: nil,
	}
	gyms[gym.Name] = newGymInternal
	go refreshRaidBossPeriodic(newGymInternal)
	if err := locationClient.AddGymLocation(*gym); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	toSend, _ := json.Marshal(gym)
	_, _ = w.Write(toSend)
}

func handleCreateRaid(w http.ResponseWriter, r *http.Request) {

	var gymId = mux.Vars(r)[api.GymIdPathVar]

	gymInternal, ok := gyms[gymId]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if gymInternal.Gym.RaidBoss == nil {
		http.Error(w, "gym has no raid boss", http.StatusBadRequest)
		return
	}

	if gymInternal.raid != nil {
		http.Error(w, "Raid already created", http.StatusConflict)
		return
	}

	if gymInternal.Gym.RaidBoss.HP <= 0 {
		http.Error(w, "Raid boss dead", http.StatusBadRequest)
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
		log.Error(err)
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
		return
	}

	authToken, err := tokens.ExtractAndVerifyAuthToken(r.Header)
	if err != nil {
		log.Error("No auth token")
		if err := conn.WriteMessage(websocket.TextMessage, []byte("No auth token")); err != nil {
			log.Error(err)
		}
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
		return
	}
	trainersClient := clients.NewTrainersClient(httpClient)
	trainerItems, statsToken, pokemonsForBattle, err := extractAndVerifyTokensForBattle(trainersClient, authToken.Username, r)
	if err != nil {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(err.Error())); err != nil {
			log.Error(err)
		}
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
		return
	}

	var gymId = mux.Vars(r)[api.GymIdPathVar]
	gymInternal, ok := gyms[gymId]
	if !ok {
		log.Error("Gym not found")
		if err := conn.WriteMessage(websocket.TextMessage, []byte("No gym found")); err != nil {
			log.Error(err)
		}
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
		return
	}

	if gymInternal.raid == nil {
		log.Error("Raid is nil")
		if err := conn.WriteMessage(websocket.TextMessage, []byte("Raid is nil")); err != nil {
			log.Error(err)
		}
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
		return
	}

	if !gymInternal.raid.started {
		gymInternal.raid.AddPlayer(authToken.Username, pokemonsForBattle, statsToken, trainerItems, conn, r.Header.Get(tokens.AuthTokenHeaderName))
	} else {
		if err := conn.WriteMessage(websocket.TextMessage, []byte("Raid already started")); err != nil {
			log.Error(err)
		}
		if err := conn.Close(); err != nil {
			log.Error(err)
		}
	}
}

func handleGetGymInfo(w http.ResponseWriter, r *http.Request) {
	var gymId = mux.Vars(r)[api.GymIdPathVar]

	gym, ok := gyms[gymId]
	if !ok {
		http.NotFound(w, r)
		return
	}

	toSend, _ := json.Marshal(gym.Gym)
	_, _ = w.Write(toSend)
}

func refreshRaidBossPeriodic(gymInternal *GymInternal) {
	gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP, config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
	log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Info("Refreshing boss...")
			gymInternal.Gym.RaidBoss = pokemons.GenerateRaidBoss(config.MaxLevel, config.StdHpDeviation, config.MaxHP, config.StdDamageDeviation, config.MaxDamage, pokemonSpecies[rand.Intn(len(pokemonSpecies))-1])
			log.Infof("New raidBoss for gym %s %v: ", gymInternal.Gym.Name, gymInternal.Gym.RaidBoss)
		}
	}
}

func extractAndVerifyTokensForBattle(trainersClient *clients.TrainersClient, username string, r *http.Request) (map[string]items.Item, *utils.TrainerStats, map[string]*pokemons.Pokemon, error) {

	pokemonTkns, err := tokens.ExtractAndVerifyPokemonTokens(r.Header)

	// pokemons

	if err != nil {
		log.Error(err)
		return nil, nil, nil, err
	}

	if len(pokemonTkns) > config.PokemonsPerRaid {
		log.Error(ErrTooManyPokemons)
		return nil, nil, nil, ErrTooManyPokemons
	}

	if len(pokemonTkns) < config.PokemonsPerRaid {
		log.Error(ErrNotEnoughPokemons)
		return nil, nil, nil, ErrNotEnoughPokemons
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
		log.Error("Invalid trainer stats token: ", err)
		return nil, nil, nil, err
	}

	if !*valid {
		log.Error("pokemon tokens not up to date")
		return nil, nil, nil, ErrInvalidPokemonHashes
	}

	// stats

	trainerStatsToken, err := tokens.ExtractAndVerifyTrainerStatsToken(r.Header)
	if err != nil {
		log.Error(err)
		return nil, nil, nil, err
	}

	valid, err = trainersClient.VerifyTrainerStats(username, trainerStatsToken.TrainerHash, r.Header.Get(tokens.AuthTokenHeaderName))

	if err != nil || !*valid {
		log.Error("Invalid trainer stats token: ", err)
		return nil, nil, nil, err
	}

	// items

	itemsToken, err := tokens.ExtractAndVerifyItemsToken(r.Header)

	if err != nil {
		log.Error(err)
		return nil, nil, nil, err
	}

	valid, err = trainersClient.VerifyItems(username, itemsToken.ItemsHash, r.Header.Get(tokens.AuthTokenHeaderName))

	if err != nil || !*valid {
		log.Error("Invalid trainer items token: ", err)
		return nil, nil, nil, err
	}

	return itemsToken.Items, &trainerStatsToken.TrainerStats, pokemonsInToken, nil
}

func loadGymsFromFile() map[string]*GymInternal {
	data, err := ioutil.ReadFile(DefaultGymsFile)
	if err != nil {
		log.Errorf("Error loading gyms file ")
		log.Fatal(err)
		panic(err)
	}

	var gymsArr []*utils.Gym
	err = json.Unmarshal(data, &gymsArr)

	if err != nil {
		log.Error(err)
		log.Panic(err)
	}

	var gymsMap = make(map[string]*GymInternal, len(gymsArr))
	for _, gym := range gymsArr {
		newGymInternal := &GymInternal{
			Gym:  gym,
			raid: nil,
		}
		gymsMap[gym.Name] = newGymInternal
		go refreshRaidBossPeriodic(newGymInternal)

		if err := locationClient.AddGymLocation(*gym); err != nil {
			log.Error("An error occurred trying to register gym location: ", err)
		}
	}

	if err != nil {
		log.Errorf("Error unmarshalling gyms")
		log.Fatal(err)
		panic(err)
	}

	log.Infof("Loaded %d gyms.", len(gymsMap))
	return gymsMap
}

func loadPokemonSpecies() []string {
	data, err := ioutil.ReadFile(PokemonsFile)
	if err != nil {
		log.Fatal("Error loading pokemons file")
		return nil
	}

	var pokemonNames []string
	err = json.Unmarshal(data, &pokemonNames)

	if err != nil {
		log.Errorf("Error unmarshalling pokemons name")
		log.Fatal(err)
	}

	log.Infof("Loaded %d pokemon species.", len(pokemonNames))

	return pokemonNames
}
