package main

import (
	"fmt"
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
	"math/rand"
	"time"
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
}

func NewRaid(raidId primitive.ObjectID, expectedCapacity int, raidBoss pokemons.Pokemon, startChan chan struct{}, client *clients.TrainersClient) *RaidInternal {
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
	}

}

func (r *RaidInternal) AddPlayer(username string, pokemons map[string]*pokemons.Pokemon, stats *utils.TrainerStats, trainerItems map[string]items.Item, trainerConn *websocket.Conn, authToken string) {
	player := &battles.TrainerBattleStatus{
		Username:        username,
		TrainerStats:    stats,
		TrainerPokemons: pokemons,
		TrainerItems:    trainerItems,
		AllPokemonsDead: false,
		UsedItems:       make(map[string]items.Item),
		CdTimer:         time.NewTimer(battles.DefaultCooldown),
	}
	log.Warn("Added player to raid")
	r.disabledTrainers = append(r.disabledTrainers, false)
	r.playersBattleStatus = append(r.playersBattleStatus, player)
	r.authTokens = append(r.authTokens, authToken)
	r.lobby.AddTrainer(username, trainerConn)
	go r.handlePlayerChannels(r.lobby.TrainersJoined - 1)
}

func (r *RaidInternal) Start() {
	startTimer := time.NewTimer(battles.TimeToStartRaid)
	<-startTimer.C
	r.started = true
	close(r.startChan)
	log.Info("Sending Start message")
	r.sendMsgToAllClients(battles.Start, []string{})
	r.issueBossMoves()
}

func (r *RaidInternal) handlePlayerChannels(i int) {
	log.Infof("Listening to channel %d", i)
	for !r.finished {
		select {
		case msgStr, ok := <-*r.lobby.TrainerInChannels[i]:
			if ok {
				r.handlePlayerMove(msgStr, r.playersBattleStatus[i], *r.lobby.TrainerOutChannels[i])
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
	r.finished = true
	r.lobby.Finished = true
	if success {
		r.commitRaidResults(r.trainersClient)
	} else {
		r.lobby.Close()
		return
	}
	r.sendMsgToAllClients(battles.Finish, []string{})
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
						battles.UpdateTrainerPokemon(*r.playersBattleStatus[i].SelectedPokemon, *r.lobby.TrainerOutChannels[i])
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
			ws.SendMessage(toSend, *r.lobby.TrainerOutChannels[i])
		}
	}
}

func (r *RaidInternal) logRaidStatus() {
	log.Info("----------------------------------------")
	log.Infof("Raid pokemon: Species : %s ; Damage: %d; HP: %d/%d;", r.raidBoss.Species, r.raidBoss.Damage, r.raidBoss.HP, r.raidBoss.MaxHP)
}

func (r *RaidInternal) handlePlayerMove(msgStr *string, issuer *battles.TrainerBattleStatus, issuerChan chan *string) {

	message, err := ws.ParseMessage(msgStr)
	if err != nil {
		errMsg := ws.Message{MsgType: battles.Error, MsgArgs: []string{battles.ErrInvalidMessageFormat.Error()}}
		ws.SendMessage(errMsg, issuerChan)
		return
	}
	switch message.MsgType {

	case battles.Attack:
		if changed, err := battles.HandleAttackMove(issuer, issuerChan, r.bossDefending, r.raidBoss); changed && err == nil {
			if r.raidBoss.HP <= 0 {
				// raid is finished
				log.Info("--------------RAID ENDED---------------")
				log.Info("Winner : players")
				r.finish(true)
			}
		}
		break
	case battles.Defend:
		if err = battles.HandleDefendMove(issuer, issuerChan); err != nil {
			log.Warnf("Player %s error %s: ", issuer.Username, err)
		}

		break

	case battles.UseItem:
		if err = battles.HandleUseItem(message, issuer, issuerChan); err != nil {
			log.Warnf("Player %s error %s: ", issuer.Username, err)
		}
		break

	case battles.SelectPokemon:
		if err = battles.HandleSelectPokemon(msgStr, issuer, issuerChan); err != nil {
			log.Warnf("Player %s error %s: ", issuer.Username, err)
		}
		break
	default:
		log.Errorf("cannot handle message type: %s ", message.MsgType)
		msg := ws.Message{MsgType: battles.Error, MsgArgs: []string{fmt.Sprintf(battles.ErrInvalidMessageType.Error())}}
		ws.SendMessage(msg, issuerChan)
		return
	}
}

func (r *RaidInternal) commitRaidResults(trainersClient *clients.TrainersClient) {
	log.Infof("Committing battle results from raid")
	for i := 0; i < r.lobby.TrainersJoined; i++ {

		if r.disabledTrainers[i] {
			continue
		}

		// Update trainer items, removing the items that were used during the battle
		if err := RemoveUsedItems(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], *r.lobby.TrainerOutChannels[i]); err != nil {
			log.Error(err)
		}

		experienceGain := experience.GetPokemonExperienceGainFromRaid(false)
		if err := UpdateTrainerPokemons(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], *r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}

		//Update trainer stats: add experience
		experienceGain = experience.GetTrainerExperienceGainFromBattle(false)
		if err := AddExperienceToPlayer(trainersClient, *r.playersBattleStatus[i], r.authTokens[i], *r.lobby.TrainerOutChannels[i], experienceGain); err != nil {
			log.Error(err)
		}
	}
}

func RemoveUsedItems(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus, authToken string, outChan chan *string) error {

	usedItems := player.UsedItems

	if len(usedItems) == 0 {
		return nil
	}

	itemIds := make([]string, 0, len(usedItems))

	for itemId := range usedItems {
		itemIds = append(itemIds, itemId)
	}

	_, err := trainersClient.RemoveItemsFromBag(player.Username, itemIds, authToken)

	if err != nil {
		return err
	}

	toSend := []string{tokens.ItemsTokenHeaderName, trainersClient.ItemsToken}
	setTokensMessage := &ws.Message{
		MsgType: battles.SetToken,
		MsgArgs: toSend,
	}
	ws.SendMessage(*setTokensMessage, outChan)

	return nil
}

func UpdateTrainerPokemons(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus, authToken string, outChan chan *string, xpAmount float64) error {

	// updates pokemon status after battle: adds XP and updates HP
	//player 0

	for id, pokemon := range player.TrainerPokemons {
		pokemon.XP += xpAmount
		pokemon.HP = pokemon.MaxHP
		_, err := trainersClient.UpdateTrainerPokemon(player.Username, id, *pokemon)
		if err != nil {
			log.Errorf("An error occurred updating pokemons from user %s : %s", player.Username, err.Error())
		}
	}

	toSend := make([]string, len(trainersClient.PokemonTokens)+1)
	toSend[0] = tokens.PokemonsTokenHeaderName
	i := 1
	for _, v := range trainersClient.PokemonTokens {
		toSend[i] = v
		i++
	}

	setTokensMessage := &ws.Message{
		MsgType: battles.SetToken,
		MsgArgs: toSend,
	}
	ws.SendMessage(*setTokensMessage, outChan)
	return nil
}

func AddExperienceToPlayer(trainersClient *clients.TrainersClient, player battles.TrainerBattleStatus, authToken string, outChan chan *string, XPAmount float64) error {

	stats := player.TrainerStats
	stats.XP += XPAmount
	_, err := trainersClient.UpdateTrainerStats(player.Username, *stats, authToken)

	if err != nil {
		return err
	}

	toSend := []string{tokens.StatsTokenHeaderName, trainersClient.TrainerStatsToken}
	setTokensMessage := &ws.Message{
		MsgType: battles.SetToken,
		MsgArgs: toSend,
	}
	ws.SendMessage(*setTokensMessage, outChan)
	return nil
}