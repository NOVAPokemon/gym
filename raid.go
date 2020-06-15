package main

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NOVAPokemon/utils"
	"github.com/NOVAPokemon/utils/clients"
	"github.com/NOVAPokemon/utils/experience"
	"github.com/NOVAPokemon/utils/items"
	"github.com/NOVAPokemon/utils/pokemons"
	"github.com/NOVAPokemon/utils/tokens"
	ws "github.com/NOVAPokemon/utils/websockets"
	"github.com/NOVAPokemon/utils/websockets/battles"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RaidInternal struct {
	trainersClient      *clients.TrainersClient
	raidBoss            *pokemons.Pokemon
	lobby               *ws.Lobby
	authTokens          []string
	playersBattleStatus []*battles.TrainerBattleStatus
	disabledTrainers    []bool
	bossDefending       bool
	cooldown            time.Duration
	failedConnections   int32
	finishOnce          sync.Once
}

func NewRaid(raidId primitive.ObjectID, capacity int, raidBoss pokemons.Pokemon, client *clients.TrainersClient, cooldownMilis int) *RaidInternal {
	return &RaidInternal{
		failedConnections:   0,
		raidBoss:            &raidBoss,
		lobby:               ws.NewLobby(raidId, capacity),
		authTokens:          make([]string, capacity),
		playersBattleStatus: make([]*battles.TrainerBattleStatus, capacity),
		disabledTrainers:    make([]bool, capacity),
		bossDefending:       false,
		trainersClient:      client,
		cooldown:            time.Duration(cooldownMilis) * time.Millisecond,
		finishOnce:          sync.Once{},
	}
}

func (r *RaidInternal) AddPlayer(username string, pokemons map[string]*pokemons.Pokemon, stats *utils.TrainerStats,
	trainerItems map[string]items.Item, trainerConn *websocket.Conn, authToken string) (int, error) {
	trainerNr, err := ws.AddTrainer(r.lobby, username, trainerConn)
	if err != nil {
		return -1, wrapRaidAddPlayerError(err)
	}

	player := &battles.TrainerBattleStatus{
		Username:        username,
		TrainerStats:    stats,
		TrainerPokemons: pokemons,
		TrainerItems:    trainerItems,
		AllPokemonsDead: false,
		UsedItems:       make(map[string]items.Item),
		CdTimer:         time.NewTimer(r.cooldown),
	}

	log.Warn("Added player to raid")
	r.disabledTrainers[trainerNr-1] = false
	r.playersBattleStatus[trainerNr-1] = player
	r.authTokens[trainerNr-1] = authToken
	go r.handlePlayerChannel(trainerNr - 1)

	return trainerNr, nil
}

func (r *RaidInternal) Start() {
	ws.StartLobby(r.lobby)
	if ws.GetTrainersJoined(r.lobby) > 0 {
		log.Info("Sending Start message")
		emitRaidStart()
		r.sendMsgToAllClients(ws.Start, []string{})
		r.issueBossMoves()
		emitRaidFinish()
	} else {
		ws.CloseLobbyConnections(r.lobby)
		return
	}
}

func (r *RaidInternal) handlePlayerChannel(i int) {
	log.Infof("Listening to channel %d", i)
	for {
		select {
		case msgStr, ok := <-r.lobby.TrainerInChannels[i]:
			if ok {
				r.handlePlayerMove(msgStr, r.playersBattleStatus[i], r.lobby.TrainerOutChannels[i])
			}
		case <-r.playersBattleStatus[i].CdTimer.C:
			r.playersBattleStatus[i].Cooldown = false
			r.playersBattleStatus[i].Defending = false

		case <-r.lobby.EndConnectionChannels[i]:
			warn := fmt.Sprintf("An error occurred with user %s", r.playersBattleStatus[i].Username)
			log.Warn(warn)
			failedNr := atomic.AddInt32(&r.failedConnections, 1)
			r.disabledTrainers[i] = true
			select {
			case <-r.lobby.Finished:
				return
			default:
				if ws.GetTrainersJoined(r.lobby) == int(failedNr) {
					r.finish(false, false)
				}
				return
			}
		case <-r.lobby.Finished:
			return
		}
	}
}

func (r *RaidInternal) finish(success bool, trainersWon bool) {
	r.finishOnce.Do(func() {
		ws.FinishLobby(r.lobby)
		if success {
			r.commitRaidResults(r.trainersClient, trainersWon)
		}

		r.sendMsgToAllClients(ws.Finish, []string{})
		for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
			<-r.lobby.EndConnectionChannels[i]
		}
		ws.CloseLobbyConnections(r.lobby)
	})
}

func (r *RaidInternal) issueBossMoves() {
	bossCooldown := 2 * time.Second
	ticker := time.NewTicker(bossCooldown)
	<-ticker.C
	for {
		select {
		case <-ticker.C:
			r.logRaidStatus()
			randNr := rand.Float64()
			var probAttack = 0.5
			if randNr < probAttack {
				log.Info("Issuing attack move...")
				for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
					if r.playersBattleStatus[i].SelectedPokemon != nil && !r.disabledTrainers[i] {
						change := battles.ApplyAttackMove(r.raidBoss, r.playersBattleStatus[i].SelectedPokemon, r.playersBattleStatus[i].Defending)
						if change {
							battles.UpdateTrainerPokemon(
								ws.NewTrackedMessage(primitive.NewObjectID()),
								*r.playersBattleStatus[i].SelectedPokemon,
								r.lobby.TrainerOutChannels[i],
								true)
							allPokemonsDead := true
							for _, pokemon := range r.playersBattleStatus[i].TrainerPokemons {
								if pokemon.HP > 0 {
									allPokemonsDead = false
									break
								}
							}
							r.playersBattleStatus[i].AllPokemonsDead = allPokemonsDead
							allTrainersDead := true
							for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
								if !r.playersBattleStatus[i].AllPokemonsDead && !r.disabledTrainers[i] {
									allTrainersDead = false
									break
								}
							}
							if allTrainersDead {
								log.Info("All trainers dead, finishing raid")
								r.finish(true, false)
								return
							}
						}
					}
				}
			} else {
				log.Info("Issuing defend move...")
				r.sendMsgToAllClients(battles.Defend, []string{})
			}
		case <-r.lobby.Finished:
			log.Warn(r.lobby.Finished)
			log.Warn("Routine issuing boss moves exiting...")
			return
		}
	}
}

func (r *RaidInternal) sendMsgToAllClients(msgType string, msgArgs []string) {
	toSend := ws.Message{MsgType: msgType, MsgArgs: msgArgs}
	for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
		if !r.disabledTrainers[i] {
			r.lobby.TrainerOutChannels[i] <- ws.GenericMsg{
				MsgType: websocket.TextMessage,
				Data:    []byte(toSend.Serialize()),
			}
		}
	}
}

func (r *RaidInternal) logRaidStatus() {
	log.Info("----------------------------------------")
	log.Infof("Raid pokemon: pokemon:ID:%s, Damage:%d, HP:%d, maxHP:%d, Species:%s", r.raidBoss.Id.Hex(), r.raidBoss.Damage, r.raidBoss.HP, r.raidBoss.MaxHP, r.raidBoss.Species)
}

func (r *RaidInternal) handlePlayerMove(msgStr *string, issuer *battles.TrainerBattleStatus, issuerChan chan ws.GenericMsg) {
	message, err := ws.ParseMessage(msgStr)
	if err != nil {
		errMsg := ws.Message{MsgType: ws.Error, MsgArgs: []string{ws.ErrorInvalidMessageFormat.Error()}}
		issuerChan <- ws.GenericMsg{
			MsgType: websocket.TextMessage,
			Data:    []byte(errMsg.Serialize()),
		}
		return
	}

	switch message.MsgType {
	case battles.Attack:
		if changed := battles.HandleAttackMove(issuer, issuerChan, r.bossDefending, r.raidBoss, r.cooldown); changed {
			if r.raidBoss.HP <= 0 {
				// raid is finished
				log.Info("--------------RAID ENDED---------------")
				log.Info("Winner : players")
				r.finish(true, true)
			}
		}
	case battles.Defend:
		battles.HandleDefendMove(issuer, issuerChan, r.cooldown)
	case battles.UseItem:
		desMsg, err := battles.DeserializeBattleMsg(message)
		if err != nil {
			log.Error(err)
			return
		}

		useItemMsg := desMsg.(*battles.UseItemMessage)
		battles.HandleUseItem(useItemMsg, issuer, issuerChan, r.cooldown)
	case battles.SelectPokemon:
		desMsg, err := battles.DeserializeBattleMsg(message)
		if err != nil {
			log.Error(err)
			return
		}

		selectPokemonMsg := desMsg.(*battles.SelectPokemonMessage)
		battles.HandleSelectPokemon(selectPokemonMsg, issuer, issuerChan)
	default:
		log.Errorf("cannot handle message type: %s ", message.MsgType)
		msg := ws.Message{MsgType: ws.Error, MsgArgs: []string{fmt.Sprintf(ws.ErrorInvalidMessageType.Error())}}
		issuerChan <- ws.GenericMsg{
			MsgType: websocket.TextMessage,
			Data:    []byte(msg.Serialize()),
		}
	}
}

func (r *RaidInternal) commitRaidResults(trainersClient *clients.TrainersClient, playersWon bool) {
	log.Infof("Committing battle results from raid")
	for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {

		if r.disabledTrainers[i] {
			continue
		}

		// Update trainer items, removing the items that were used during the battle
		if err := RemoveUsedItems(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i]); err != nil {
			log.Error(err)
		}

		experienceGain := experience.GetPokemonExperienceGainFromRaid(playersWon)
		if err := UpdateTrainerPokemons(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}

		// Update trainer stats: add experience
		experienceGain = experience.GetTrainerExperienceGainFromBattle(playersWon)
		if err := AddExperienceToPlayer(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}
	}
}

func RemoveUsedItems(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan chan ws.GenericMsg) error {

	usedItems := player.UsedItems

	if len(usedItems) == 0 {
		return nil
	}

	itemIds := make([]string, 0, len(usedItems))

	for itemId := range usedItems {
		itemIds = append(itemIds, itemId)
	}

	_, err := trainersClient.RemoveItems(player.Username, itemIds, authToken)
	if err != nil {
		return err
	}

	setTokensMessage := ws.SetTokenMessage{
		TokenField:   tokens.ItemsTokenHeaderName,
		TokensString: []string{trainersClient.ItemsToken},
	}.SerializeToWSMessage()
	outChan <- ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	}
	return nil
}

func UpdateTrainerPokemons(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan chan ws.GenericMsg, xpAmount float64) error {

	// updates pokemon status after battle: adds XP and updates HP
	// player 0

	for id, pokemon := range player.TrainerPokemons {
		pokemon.XP += xpAmount
		pokemon.HP = pokemon.MaxHP
		_, err := trainersClient.UpdateTrainerPokemon(player.Username, id, *pokemon, authToken)
		if err != nil {
			log.Errorf("An error occurred updating pokemons from user %s : %s", player.Username, err.Error())
		}
	}

	toSend := make([]string, len(trainersClient.PokemonTokens))
	i := 0
	for _, v := range trainersClient.PokemonTokens {
		toSend[i] = v
		i++
	}

	setTokensMessage := ws.SetTokenMessage{
		TokenField:   tokens.PokemonsTokenHeaderName,
		TokensString: toSend,
	}.SerializeToWSMessage()

	outChan <- ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	}

	return nil
}

func AddExperienceToPlayer(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan chan ws.GenericMsg, XPAmount float64) error {

	stats := player.TrainerStats
	stats.XP += XPAmount
	_, err := trainersClient.UpdateTrainerStats(player.Username, *stats, authToken)

	if err != nil {
		return err
	}

	setTokensMessage := ws.SetTokenMessage{
		TokenField:   tokens.StatsTokenHeaderName,
		TokensString: []string{trainersClient.TrainerStatsToken},
	}.SerializeToWSMessage()
	outChan <- ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	}

	return nil
}
