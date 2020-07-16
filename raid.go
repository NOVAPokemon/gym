package main

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NOVAPokemon/utils/comms_manager"
	"github.com/pkg/errors"

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

type raidInternal struct {
	trainersClient           *clients.TrainersClient
	raidBoss                 *pokemons.Pokemon
	lobby                    *ws.Lobby
	authTokens               []string
	playerBattleStatusLocks  []sync.Mutex
	playersBattleStatus      []*battles.TrainerBattleStatus
	bossDefending            bool
	cooldown                 time.Duration
	failedConnections        int32
	bossLock                 sync.Mutex
	trainersListenRoutinesWg sync.WaitGroup
	raidOver                 chan struct{}
	commsManager             comms_manager.CommunicationManager
}

func newRaid(raidId primitive.ObjectID, capacity int, raidBoss pokemons.Pokemon, client *clients.TrainersClient,
	cooldownMilis int, commsManager comms_manager.CommunicationManager) *raidInternal {
	return &raidInternal{
		failedConnections:        0,
		lobby:                    ws.NewLobby(raidId, capacity),
		authTokens:               make([]string, capacity),
		playersBattleStatus:      make([]*battles.TrainerBattleStatus, capacity),
		playerBattleStatusLocks:  make([]sync.Mutex, capacity),
		trainersListenRoutinesWg: sync.WaitGroup{},
		bossLock:                 sync.Mutex{},
		raidBoss:                 &raidBoss,
		bossDefending:            false,
		trainersClient:           client,
		cooldown:                 time.Duration(cooldownMilis) * time.Millisecond,
		raidOver:                 make(chan struct{}),
		commsManager:             commsManager,
	}
}

func (r *raidInternal) addPlayer(username string, pokemons map[string]*pokemons.Pokemon, stats *utils.TrainerStats,
	trainerItems map[string]items.Item, trainerConn *websocket.Conn, authToken string) (int, error) {
	trainerNr, err := ws.AddTrainer(r.lobby, username, trainerConn, commsManager)
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
	r.playerBattleStatusLocks[trainerNr-1] = sync.Mutex{}
	r.playersBattleStatus[trainerNr-1] = player
	r.authTokens[trainerNr-1] = authToken
	r.handlePlayerChannel(trainerNr - 1)
	return trainerNr - 1, nil
}

func (r *raidInternal) start() {
	ws.StartLobby(r.lobby)
	if ws.GetTrainersJoined(r.lobby) > 0 {
		log.Info("Sending Start message")
		emitRaidStart()
		r.sendMsgToAllClients(ws.StartMessage{})
		trainersWon, err := r.issueBossMoves()
		if err != nil {
			log.Error(err)
			ws.FinishLobby(r.lobby)
		} else {
			r.finish(trainersWon)
		}
		emitRaidFinish()
	} else {
		ws.FinishLobby(r.lobby)
	}
}

func (r *raidInternal) handlePlayerChannel(i int) {
	log.Infof("Listening to channel %d", i)
	r.trainersListenRoutinesWg.Add(1)
	defer r.trainersListenRoutinesWg.Done()
	for {
		select {
		case msgStr, ok := <-r.lobby.TrainerInChannels[i]:
			if ok {
				r.playerBattleStatusLocks[i].Lock()
				r.handlePlayerMove(msgStr, r.playersBattleStatus[i], r.lobby.TrainerOutChannels[i])
				r.playerBattleStatusLocks[i].Unlock()
			}
		case <-r.playersBattleStatus[i].CdTimer.C:
			r.playerBattleStatusLocks[i].Lock()
			r.playersBattleStatus[i].Cooldown = false
			r.playersBattleStatus[i].Defending = false
			r.playerBattleStatusLocks[i].Unlock()
		case <-r.lobby.DoneListeningFromConn[i]:
			r.handlePlayerLeave(i)
			return
		case <-r.lobby.DoneWritingToConn[i]:
			r.handlePlayerLeave(i)
			return
		case <-r.lobby.Finished:
			log.Error("handler routine for trainer moves exited unexpectedly")
			return
		case <-r.raidOver:
			log.Info("handler routine for trainer moves exited as expected")
			return
		}
	}
}

func (r *raidInternal) handlePlayerLeave(playerNr int) {
	log.Warnf("An error occurred with user %s", r.playersBattleStatus[playerNr].Username)
	atomic.AddInt32(&r.failedConnections, 1)
}

func (r *raidInternal) finish(trainersWon bool) {
	close(r.raidOver)
	log.Info("Waiting for routines handling trainer moves...")
	r.trainersListenRoutinesWg.Wait()
	log.Info("Done!")
	r.commitRaidResults(r.trainersClient, trainersWon)
	r.sendMsgToAllClients(ws.FinishMessage{})
	wg := sync.WaitGroup{}
	for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
		wg.Add(1)
		trainerNr := i
		go func() {
			defer wg.Done()
			select {
			case <-r.lobby.DoneListeningFromConn[trainerNr]:
			case <-time.After(3 * time.Second):
			}
		}()
	}
	wg.Wait()
	ws.FinishLobby(r.lobby)
}

func (r *raidInternal) issueBossMoves() (bool, error) {
	bossCooldown := 2 * time.Second
	ticker := time.NewTicker(bossCooldown)
	<-ticker.C
	for {
		select {
		case <-ticker.C:

			r.bossLock.Lock()
			if r.raidBoss.HP <= 0 {
				r.bossLock.Unlock()
				return true, nil
			}
			r.bossLock.Unlock()

			numFailedConnections := int(atomic.LoadInt32(&r.failedConnections))
			if ws.GetTrainersJoined(r.lobby) == numFailedConnections {
				return false, errors.New("All trainers left raid")
			}

			randNr := rand.Float64()
			var probAttack = 0.5
			if randNr < probAttack {
				log.Info("Issuing attack move...")
				r.bossLock.Lock()
				r.bossDefending = false
				r.bossLock.Unlock()
				for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
					select {
					case <-r.lobby.DoneListeningFromConn[i]:
					case <-r.lobby.DoneWritingToConn[i]:
					default:
						r.playerBattleStatusLocks[i].Lock()
						if r.playersBattleStatus[i].SelectedPokemon != nil {
							change := battles.ApplyAttackMove(r.raidBoss, r.playersBattleStatus[i].SelectedPokemon, r.playersBattleStatus[i].Defending)
							if change {
								battles.UpdateTrainerPokemon(
									ws.NewTrackedMessage(primitive.NewObjectID()),
									*r.playersBattleStatus[i].SelectedPokemon,
									r.lobby.TrainerOutChannels[i],
									true)
								allPokemonsDead := r.playersBattleStatus[i].AreAllPokemonsDead()
								r.playersBattleStatus[i].AllPokemonsDead = allPokemonsDead
								if allPokemonsDead {
									allTrainersDead := true
									for j := 0; j < ws.GetTrainersJoined(r.lobby); j++ {
										// no need to lock other status because no other routine changes AllPokemonsDead field
										if !r.playersBattleStatus[j].AllPokemonsDead {
											allTrainersDead = false
											break
										}
									}
									if allTrainersDead {
										log.Info("All trainers dead, finishing raid")
										r.playerBattleStatusLocks[i].Unlock()
										return false, nil
									}
								}
							}
						}
						r.playerBattleStatusLocks[i].Unlock()
					}
				}
			} else {
				log.Info("Issuing defend move...")
				r.bossLock.Lock()
				r.bossDefending = true
				r.bossLock.Unlock()
				r.sendMsgToAllClients(battles.DefendMessage{})
			}
		case <-r.lobby.Finished:
			log.Warn("Routine issuing boss moves exiting unexpectedly...")
		}
	}
}

func (r *raidInternal) sendMsgToAllClients(msg ws.Serializable) {
	for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
		select {
		case <-r.lobby.DoneListeningFromConn[i]:
		case <-r.lobby.DoneWritingToConn[i]:
		case r.lobby.TrainerOutChannels[i] <- msg:
		}
	}
}

func (r *raidInternal) handlePlayerMove(msgStr string, issuer *battles.TrainerBattleStatus,
	issuerChan chan ws.Serializable) {
	message, err := ws.ParseMessage(msgStr)
	if err != nil {
		errMsg := ws.ErrorMessage{
			Info:  ws.ErrorInvalidMessageFormat.Error(),
			Fatal: false,
		}
		issuerChan <- errMsg
		return
	}

	var desMsg ws.Serializable

	switch message.MsgType {
	case battles.Attack:
		r.bossLock.Lock()
		battles.HandleAttackMove(issuer, issuerChan, r.bossDefending, r.raidBoss, r.cooldown)
		r.bossLock.Unlock()
	case battles.Defend:
		battles.HandleDefendMove(issuer, issuerChan, r.cooldown)
	case battles.UseItem:
		desMsg, err = battles.DeserializeBattleMsg(message)
		if err != nil {
			log.Error(err)
			return
		}
		useItemMsg := desMsg.(*battles.UseItemMessage)
		battles.HandleUseItem(useItemMsg, issuer, issuerChan, r.cooldown)
	case battles.SelectPokemon:
		desMsg, err = battles.DeserializeBattleMsg(message)
		if err != nil {
			log.Error(err)
			return
		}

		selectPokemonMsg := desMsg.(*battles.SelectPokemonMessage)
		battles.HandleSelectPokemon(selectPokemonMsg, issuer, issuerChan)
	default:
		log.Errorf("cannot handle message type: %s ", message.MsgType)
		msg := ws.ErrorMessage{
			Info:  ws.ErrorInvalidMessageType.Error(),
			Fatal: false,
		}
		issuerChan <- msg
	}
}

func (r *raidInternal) commitRaidResults(trainersClient *clients.TrainersClient, playersWon bool) {
	log.Infof("Committing battle results from raid")
	var wg sync.WaitGroup
	for i := 0; i < ws.GetTrainersJoined(r.lobby); i++ {
		select {
		case <-r.lobby.DoneListeningFromConn[i]:
		case <-r.lobby.DoneWritingToConn[i]:
		default:
			wg.Add(1)
			trainerNr := i
			tempClient := clients.NewTrainersClient(trainersClient.HttpClient, commsManager)
			go r.commitRaidResultsForTrainer(tempClient, trainerNr, playersWon, &wg)
		}
	}
	wg.Wait()
}

func (r *raidInternal) commitRaidResultsForTrainer(trainersClient *clients.TrainersClient, trainerNr int,
	trainersWon bool, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Infof("Committing battle results from raid")
	r.playerBattleStatusLocks[trainerNr].Lock()
	defer r.playerBattleStatusLocks[trainerNr].Unlock()

	// Update trainer items, removing the items that were used during the battle
	if err := removeUsedItems(trainersClient, r.playersBattleStatus[trainerNr], r.authTokens[trainerNr],
		r.lobby.TrainerOutChannels[trainerNr]); err != nil {
		log.Error(err)
	}

	experienceGain := experience.GetPokemonExperienceGainFromRaid(trainersWon)
	if err := updateTrainerPokemons(trainersClient, r.playersBattleStatus[trainerNr], r.authTokens[trainerNr],
		r.lobby.TrainerOutChannels[trainerNr], experienceGain); err != nil {
		log.Error(err)
	}

	// Update trainer stats: add experience
	experienceGain = experience.GetTrainerExperienceGainFromBattle(trainersWon)
	if err := addExperienceToPlayer(trainersClient, r.playersBattleStatus[trainerNr], r.authTokens[trainerNr],
		r.lobby.TrainerOutChannels[trainerNr], experienceGain); err != nil {
		log.Error(err)
	}
}

func removeUsedItems(trainersClient *clients.TrainersClient, player *battles.TrainerBattleStatus,
	authToken string, outChan chan ws.Serializable) error {

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
	}
	outChan <- setTokensMessage

	return nil
}

func updateTrainerPokemons(trainersClient *clients.TrainersClient, player *battles.TrainerBattleStatus,
	authToken string, outChan chan ws.Serializable, xpAmount float64) error {

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
	}

	outChan <- setTokensMessage

	return nil
}

func addExperienceToPlayer(trainersClient *clients.TrainersClient, player *battles.TrainerBattleStatus,
	authToken string, outChan chan ws.Serializable, XPAmount float64) error {

	stats := player.TrainerStats
	stats.XP += XPAmount
	_, err := trainersClient.UpdateTrainerStats(player.Username, *stats, authToken)

	if err != nil {
		return err
	}

	setTokensMessage := ws.SetTokenMessage{
		TokenField:   tokens.StatsTokenHeaderName,
		TokensString: []string{trainersClient.TrainerStatsToken},
	}
	outChan <- setTokensMessage

	return nil
}
