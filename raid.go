package main

import (
	"fmt"
	"math/rand"
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
	lobby               *ws.RaidLobby
	authTokens          []string
	playersBattleStatus []*battles.TrainerBattleStatus
	disabledTrainers    []bool
	started             bool
	finished            bool
	bossDefending       bool
	startChan           chan struct{}
	cooldown            time.Duration
	waitTime            time.Duration
}

func NewRaid(raidId primitive.ObjectID, expectedCapacity int, raidBoss pokemons.Pokemon, startChan chan struct{},
	client *clients.TrainersClient, cooldownMilis int, waitTimeMilis int) *RaidInternal {
	return &RaidInternal{
		raidBoss:            &raidBoss,
		lobby:               ws.NewRaidLobby(raidId, expectedCapacity),
		authTokens:          make([]string, 0, expectedCapacity),
		playersBattleStatus: make([]*battles.TrainerBattleStatus, 0, expectedCapacity),
		disabledTrainers:    make([]bool, 0, expectedCapacity),
		started:             false,
		finished:            false,
		startChan:           startChan,
		bossDefending:       false,
		trainersClient:      client,
		cooldown:            time.Duration(cooldownMilis) * time.Millisecond,
		waitTime:            time.Duration(waitTimeMilis) * time.Millisecond,
	}

}

func (r *RaidInternal) AddPlayer(username string, pokemons map[string]*pokemons.Pokemon, stats *utils.TrainerStats,
	trainerItems map[string]items.Item, trainerConn *websocket.Conn, authToken string) {
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
	r.disabledTrainers = append(r.disabledTrainers, false)
	r.playersBattleStatus = append(r.playersBattleStatus, player)
	r.authTokens = append(r.authTokens, authToken)
	r.lobby.AddTrainer(username, trainerConn)
	go r.handlePlayerChannels(r.lobby.TrainersJoined - 1)
}

func (r *RaidInternal) Start() {
	startTimer := time.NewTimer(r.waitTime)
	<-startTimer.C
	r.started = true
	close(r.startChan)
	if r.lobby.TrainersJoined > 0 {
		log.Info("Sending Start message")
		emitRaidStart()
		r.sendMsgToAllClients(ws.Start, []string{})
		r.issueBossMoves()
		emitRaidFinish()
	} else {
		r.lobby.Close()
		return
	}
}

func (r *RaidInternal) handlePlayerChannels(i int) {
	log.Infof("Listening to channel %d", i)
	for !r.finished {
		select {
		case msgStr, ok := <-*r.lobby.TrainerInChannels[i]:
			if ok {
				r.handlePlayerMove(msgStr, r.playersBattleStatus[i], r.lobby.TrainerOutChannels[i])
			}
		case <-r.playersBattleStatus[i].CdTimer.C:
			r.playersBattleStatus[i].Cooldown = false
			r.playersBattleStatus[i].Defending = false

		case <-r.lobby.EndConnectionChannels[i]:
			warn := fmt.Sprintf("An error occurred with user %s", r.playersBattleStatus[i].Username)
			log.Warn(warn)
			r.lobby.ActiveConnections--
			r.disabledTrainers[i] = true
			if r.lobby.ActiveConnections == 0 && !r.finished {
				r.finish(false)
			}
			return
		}
	}
}

func (r *RaidInternal) finish(success bool) {
	r.lobby.Finished = true
	if success {
		r.commitRaidResults(r.trainersClient)
	} else {
		r.lobby.Close()
		return
	}
	r.sendMsgToAllClients(ws.Finish, []string{})
	for i := 0; i < r.lobby.TrainersJoined; i++ {
		<-r.lobby.EndConnectionChannels[i]
	}
}

func (r *RaidInternal) issueBossMoves() {
	bossCooldown := 2 * time.Second
	ticker := time.NewTicker(bossCooldown)
	<-ticker.C
	for !r.finished {
		r.logRaidStatus()
		randNr := rand.Float64()
		var probAttack = 0.5
		if randNr < probAttack {
			log.Info("Issuing attack move...")
			for i := 0; i < r.lobby.TrainersJoined; i++ {
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
						for i := 0; i < r.lobby.TrainersJoined; i++ {
							if !r.playersBattleStatus[i].AllPokemonsDead && !r.disabledTrainers[i] {
								allTrainersDead = false
								break
							}
						}
						if allTrainersDead {
							log.Info("All trainers dead, finishing raid")
							r.finish(true)
							return
						}
					}
				}
			}
		} else {
			log.Info("Issuing defend move...")
			r.sendMsgToAllClients(battles.Defend, []string{})
		}
		<-ticker.C
	}
	log.Warn(r.finished)
	log.Warn("Routine issuing boss moves exiting...")
}

func (r *RaidInternal) sendMsgToAllClients(msgType string, msgArgs []string) {
	toSend := ws.Message{MsgType: msgType, MsgArgs: msgArgs}
	for i := 0; i < r.lobby.TrainersJoined; i++ {
		if !r.disabledTrainers[i] {
			_ = r.lobby.TrainerOutChannels[i].Write(ws.GenericMsg{
				MsgType: websocket.TextMessage,
				Data:    []byte(toSend.Serialize()),
			})
		}
	}
}

func (r *RaidInternal) logRaidStatus() {
	log.Info("----------------------------------------")
	log.Infof("Raid pokemon: pokemon:ID:%s, Damage:%d, HP:%d, maxHP:%d, Species:%s", r.raidBoss.Id.Hex(), r.raidBoss.Damage, r.raidBoss.HP, r.raidBoss.MaxHP, r.raidBoss.Species)
}

func (r *RaidInternal) handlePlayerMove(msgStr *string, issuer *battles.TrainerBattleStatus, issuerChan *ws.SyncChannel) {
	message, err := ws.ParseMessage(msgStr)
	if err != nil {
		errMsg := ws.Message{MsgType: ws.Error, MsgArgs: []string{ws.ErrorInvalidMessageFormat.Error()}}
		_ = issuerChan.Write(ws.GenericMsg{
			MsgType: websocket.TextMessage,
			Data:    []byte(errMsg.Serialize()),
		})
		return
	}

	switch message.MsgType {
	case battles.Attack:
		if changed := battles.HandleAttackMove(issuer, issuerChan, r.bossDefending, r.raidBoss, r.cooldown); changed {
			if r.raidBoss.HP <= 0 {
				// raid is finished
				log.Info("--------------RAID ENDED---------------")
				log.Info("Winner : players")
				r.finish(true)
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
		_ = issuerChan.Write(ws.GenericMsg{
			MsgType: websocket.TextMessage,
			Data:    []byte(msg.Serialize()),
		})
	}
}

func (r *RaidInternal) commitRaidResults(trainersClient *clients.TrainersClient) {
	log.Infof("Committing battle results from raid")
	for i := 0; i < r.lobby.TrainersJoined; i++ {

		if r.disabledTrainers[i] {
			continue
		}

		// Update trainer items, removing the items that were used during the battle
		if err := RemoveUsedItems(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i]); err != nil {
			log.Error(err)
		}

		experienceGain := experience.GetPokemonExperienceGainFromRaid(false)
		if err := UpdateTrainerPokemons(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}

		// Update trainer stats: add experience
		experienceGain = experience.GetTrainerExperienceGainFromBattle(false)
		if err := AddExperienceToPlayer(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}
	}
}

func RemoveUsedItems(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan *ws.SyncChannel) error {

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
	err = outChan.Write(ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	})

	if err != nil {
		return err
	}

	return nil
}

func UpdateTrainerPokemons(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan *ws.SyncChannel, xpAmount float64) error {

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
	err := outChan.Write(ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	})

	if err != nil {
		return err
	}

	return nil
}

func AddExperienceToPlayer(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus,
	authToken string, outChan *ws.SyncChannel, XPAmount float64) error {

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
	err = outChan.Write(ws.GenericMsg{
		MsgType: websocket.TextMessage,
		Data:    []byte(setTokensMessage.Serialize()),
	})

	if err != nil {
		return err
	}

	return nil
}
